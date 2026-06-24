package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/coalaura/lugo-min/minifier"

	"github.com/coalaura/lugo/ast"
	"github.com/coalaura/lugo/parser"
	"github.com/urfave/cli/v3"
)

var command = &cli.Command{
	Name:        "lugo-min",
	Usage:       "Blazingly fast Lua code minifier",
	Description: "lugo-min is a high-performance Lua code minifier built on top of the lugo AST architecture.",
	ArgsUsage:   "[file...]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "write",
			Aliases: []string{"w"},
			Usage:   "Overwrite existing files in-place",
		},
		&cli.StringFlag{
			Name:    "output",
			Aliases: []string{"o"},
			Usage:   "Write output to a specific file or directory",
		},
		&cli.BoolFlag{
			Name:  "no-rename",
			Usage: "Disable local variable renaming",
		},
		&cli.BoolFlag{
			Name:  "cache-globals",
			Usage: "Cache global functions and constants at the top of the file",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "optimize-loops",
			Usage: "Refactor generic ipairs loops to numeric loops",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "const-fold",
			Usage: "Perform compile-time optimization of static expressions",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "combine-locals",
			Usage: "Combine consecutive local definitions into a single statement",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "optimize-table-insert",
			Usage: "Refactor table.insert(t, v) to t[#t+1] = v",
			Value: true,
		},
		&cli.IntFlag{
			Name:  "global-threshold",
			Usage: "Minimum usage count before a global namespace gets cached",
			Value: 1,
		},
		&cli.IntFlag{
			Name:  "max-locals",
			Usage: "Maximum root-scope locals after global caching; 0 disables the cap",
			Value: 150,
		},
		&cli.StringSliceFlag{
			Name:  "reserved-names",
			Usage: "Comma-separated list of names that should never be used as minified local names",
		},
		&cli.BoolFlag{
			Name:  "no-shadow-all-globals",
			Usage: "Prevent renaming local variables to any standard Lua global name",
		},
		&cli.BoolFlag{
			Name:  "no-shadow-referenced-globals",
			Usage: "Prevent renaming local variables to any global name referenced in the script",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "obfuscate-events",
			Usage: "Obfuscate FiveM event names by replacing them with short identifiers",
			Value: true,
		},
		&cli.StringSliceFlag{
			Name:  "event-functions",
			Usage: "Comma-separated list of additional event functions beyond the defaults",
		},
		&cli.BoolFlag{
			Name:  "shorten-numbers",
			Usage: "Shorten numeric literals (0.5 -> .5, 5.0 -> 5)",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "fold-gethashkey",
			Usage: "Fold GetHashKey(\"string\") calls to their JOAAT hash at compile time",
		},
		&cli.BoolFlag{
			Name:  "simplify-citizen",
			Usage: "Simplify Citizen.Wait(x) to Wait(x) and Citizen.CreateThread(f) to CreateThread(f)",
		},
		&cli.BoolFlag{
			Name:  "fixpoint",
			Usage: "Run optimization passes repeatedly until no more changes are detected",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "fold-string-concat",
			Usage: "Fold constant string concatenation (\"a\" .. \"b\" -> \"ab\")",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "fold-unary",
			Usage: "Fold constant unary expressions (not true -> false, -(-x) -> x, #\"str\" -> len)",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "fold-logical",
			Usage: "Fold logical short-circuit expressions (true and x -> x, false or x -> x)",
		},
		&cli.BoolFlag{
			Name:  "dead-code",
			Usage: "Eliminate dead code (unreachable statements, if false then ... end)",
		},
		&cli.StringSliceFlag{
			Name:  "rename-calls",
			Usage: "Rename function calls (old=new,old2=new2)",
		},
		&cli.StringSliceFlag{
			Name:  "skip-event-strings-in",
			Usage: "Comma-separated list of function names where event string replacement should be skipped",
		},
	},
	Action: runMinify,
}

func main() {
	if err := command.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Execution error: %v\n", err)

		os.Exit(1)
	}
}

func runMinify(ctx context.Context, cmd *cli.Command) error {
	var eventState *minifier.EventState

	if cmd.Bool("obfuscate-events") {
		functions := make(map[string]bool)

		for _, f := range minifier.DefaultEventFunctions {
			functions[f] = true
		}

		for _, f := range cmd.StringSlice("event-functions") {
			functions[f] = true
		}

		eventState = minifier.NewEventState(functions)
	}

	args := cmd.Args().Slice()

	if len(args) == 0 {
		src, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("failed to read from stdin: %w", err)
		}

		out, err := minifySource(cmd, src, !cmd.Bool("no-rename"), eventState)
		if err != nil {
			return err
		}

		_, err = os.Stdout.Write(out)
		return err
	}

	for _, file := range args {
		src, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", file, err)
		}

		out, err := minifySource(cmd, src, !cmd.Bool("no-rename"), eventState)
		if err != nil {
			return fmt.Errorf("failed to minify %s: %w", file, err)
		}

		if cmd.Bool("write") {
			err = os.WriteFile(file, out, 0644)
			if err != nil {
				return fmt.Errorf("failed to write file %s: %w", file, err)
			}
		} else if output := cmd.String("output"); output != "" {
			var dest string

			if len(args) > 1 {
				err = os.MkdirAll(output, 0755)
				if err != nil {
					return fmt.Errorf("failed to create directory %s: %w", output, err)
				}

				dest = filepath.Join(output, filepath.Base(file))
			} else {
				info, err := os.Stat(output)
				if err == nil && info.IsDir() {
					dest = filepath.Join(output, filepath.Base(file))
				} else {
					dest = output
				}
			}

			err = os.WriteFile(dest, out, 0644)
			if err != nil {
				return fmt.Errorf("failed to write target %s: %w", dest, err)
			}
		} else {
			if len(args) > 1 {
				fmt.Printf("--- %s ---\n", file)
			}

			_, err = os.Stdout.Write(out)
			if err != nil {
				return err
			}

			if len(args) > 1 {
				fmt.Println()
			}
		}
	}

	if eventState != nil {
		data, err := json.MarshalIndent(eventState.Map, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal event map: %w", err)
		}

		fmt.Println(string(data))
	}

	return nil
}

func minifySource(cmd *cli.Command, src []byte, renameLocals bool, eventState *minifier.EventState) ([]byte, error) {
	tree := ast.NewTree(src)

	p := parser.New(src, tree, 50)

	p.Parse()

	if len(p.Errors) > 0 {
		first := p.Errors[0]

		line, col := tree.Position(first.Start)

		return nil, fmt.Errorf("syntax error at line %d, col %d: %s", line+1, col+1, first.Message)
	}

	resolver := minifier.NewResolver(tree, renameLocals)

	resolver.NoShadowAllGlobals = cmd.Bool("no-shadow-all-globals")
	resolver.NoShadowRefGlobals = cmd.Bool("no-shadow-referenced-globals")

	for _, name := range cmd.StringSlice("reserved-names") {
		resolver.ReservedNames[name] = true
	}

	resolver.Resolve()

	renameCalls := make(map[string]string)

	for _, pair := range cmd.StringSlice("rename-calls") {
		parts := strings.SplitN(pair, "=", 2)

		if len(parts) == 2 {
			renameCalls[parts[0]] = parts[1]
		}
	}

	skipEventContexts := make(map[string]bool)

	for _, name := range cmd.StringSlice("skip-event-strings-in") {
		skipEventContexts[name] = true
	}

	opts := minifier.OptimizerOptions{
		CacheGlobals:        cmd.Bool("cache-globals"),
		OptimizeLoops:       cmd.Bool("optimize-loops"),
		ConstantFolding:     cmd.Bool("const-fold"),
		CombineLocals:       cmd.Bool("combine-locals"),
		OptimizeTableInsert: cmd.Bool("optimize-table-insert"),
		GlobalThreshold:     cmd.Int("global-threshold"),
		MaxLocals:           cmd.Int("max-locals"),

		FoldGetHashKey:    cmd.Bool("fold-gethashkey"),
		SimplifyCitizen:   cmd.Bool("simplify-citizen"),
		Fixpoint:          cmd.Bool("fixpoint"),
		FoldStringConcat:  cmd.Bool("fold-string-concat"),
		FoldUnary:         cmd.Bool("fold-unary"),
		FoldLogical:       cmd.Bool("fold-logical"),
		DeadCode:          cmd.Bool("dead-code"),
		RenameCalls:       renameCalls,
		SkipEventContexts: skipEventContexts,
	}

	optimizer := minifier.NewOptimizer(tree, resolver.IdentMap, eventState, opts)

	optimizer.Optimize()

	resolver = minifier.NewResolver(tree, renameLocals)

	resolver.NoShadowAllGlobals = cmd.Bool("no-shadow-all-globals")
	resolver.NoShadowRefGlobals = cmd.Bool("no-shadow-referenced-globals")

	for _, name := range cmd.StringSlice("reserved-names") {
		resolver.ReservedNames[name] = true
	}

	resolver.Resolve()

	printer := minifier.NewMinifier(tree, resolver.IdentMap)

	printer.ShortenNumbers = cmd.Bool("shorten-numbers")

	return printer.Minify(), nil
}
