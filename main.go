package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/coalaura/lugo-min/minifier"

	"github.com/coalaura/lugo/ast"
	"github.com/coalaura/lugo/parser"
	"github.com/spf13/cobra"
)

var (
	flagWrite               bool
	flagOutput              string
	flagNoRename            bool
	flagCacheGlobals        bool
	flagOptimizeLoops       bool
	flagConstFold           bool
	flagCombineLocals       bool
	flagOptimizeTableInsert bool
	flagGlobalThreshold     int
	flagMaxLocals           int
	flagReservedNames       []string
	flagNoShadowAllGlobals  bool
	flagNoShadowRefGlobals  bool
)

var command = &cobra.Command{
	Use:   "lugo-min [file...]",
	Short: "Blazingly fast Lua code minifier",
	Long:  "lugo-min is a high-performance Lua code minifier built on top of the lugo AST architecture.",
	RunE:  runMinify,
}

func init() {
	flags := command.Flags()

	flags.BoolVarP(&flagWrite, "write", "w", false, "Overwrite existing files in-place")
	flags.StringVarP(&flagOutput, "output", "o", "", "Write output to a specific file or directory")
	flags.BoolVar(&flagNoRename, "no-rename", false, "Disable local variable renaming")
	flags.BoolVar(&flagCacheGlobals, "cache-globals", true, "Cache global functions and constants at the top of the file")
	flags.BoolVar(&flagOptimizeLoops, "optimize-loops", true, "Refactor generic ipairs loops to numeric loops")
	flags.BoolVar(&flagConstFold, "const-fold", true, "Perform compile-time optimization of static expressions")
	flags.BoolVar(&flagCombineLocals, "combine-locals", true, "Combine consecutive local definitions into a single statement")
	flags.BoolVar(&flagOptimizeTableInsert, "optimize-table-insert", true, "Refactor table.insert(t, v) to t[#t+1] = v")
	flags.IntVar(&flagGlobalThreshold, "global-threshold", 1, "Minimum usage count before a global namespace gets cached")
	flags.IntVar(&flagMaxLocals, "max-locals", 150, "Maximum root-scope locals after global caching; 0 disables the cap")
	flags.StringSliceVar(&flagReservedNames, "reserved-names", nil, "Comma-separated list of names that should never be used as minified local names")
	flags.BoolVar(&flagNoShadowAllGlobals, "no-shadow-all-globals", false, "Prevent renaming local variables to any standard Lua global name")
	flags.BoolVar(&flagNoShadowRefGlobals, "no-shadow-referenced-globals", true, "Prevent renaming local variables to any global name referenced in the script")
}

func main() {
	err := command.Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Execution error: %v\n", err)

		os.Exit(1)
	}
}

func runMinify(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		src, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("failed to read from stdin: %w", err)
		}

		out, err := minifySource(src, !flagNoRename)
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

		out, err := minifySource(src, !flagNoRename)
		if err != nil {
			return fmt.Errorf("failed to minify %s: %w", file, err)
		}

		if flagWrite {
			err = os.WriteFile(file, out, 0644)
			if err != nil {
				return fmt.Errorf("failed to write file %s: %w", file, err)
			}
		} else if flagOutput != "" {
			var dest string

			if len(args) > 1 {
				err = os.MkdirAll(flagOutput, 0755)
				if err != nil {
					return fmt.Errorf("failed to create directory %s: %w", flagOutput, err)
				}

				dest = filepath.Join(flagOutput, filepath.Base(file))
			} else {
				info, err := os.Stat(flagOutput)
				if err == nil && info.IsDir() {
					dest = filepath.Join(flagOutput, filepath.Base(file))
				} else {
					dest = flagOutput
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

	return nil
}

func minifySource(src []byte, renameLocals bool) ([]byte, error) {
	tree := ast.NewTree(src)

	p := parser.New(src, tree, 50)

	p.Parse()

	if len(p.Errors) > 0 {
		first := p.Errors[0]

		line, col := tree.Position(first.Start)

		return nil, fmt.Errorf("syntax error at line %d, col %d: %s", line+1, col+1, first.Message)
	}

	resolver := minifier.NewResolver(tree, renameLocals)

	resolver.NoShadowAllGlobals = flagNoShadowAllGlobals
	resolver.NoShadowRefGlobals = flagNoShadowRefGlobals

	if len(flagReservedNames) > 0 {
		for _, name := range flagReservedNames {
			resolver.ReservedNames[name] = true
		}
	}

	resolver.Resolve()

	optimizer := minifier.NewOptimizer(
		tree,
		resolver.IdentMap,
		flagCacheGlobals,
		flagOptimizeLoops,
		flagConstFold,
		flagCombineLocals,
		flagOptimizeTableInsert,
		flagGlobalThreshold,
		flagMaxLocals,
	)

	optimizer.Optimize()

	resolver = minifier.NewResolver(tree, renameLocals)

	resolver.NoShadowAllGlobals = flagNoShadowAllGlobals
	resolver.NoShadowRefGlobals = flagNoShadowRefGlobals

	if len(flagReservedNames) > 0 {
		for _, name := range flagReservedNames {
			resolver.ReservedNames[name] = true
		}
	}

	resolver.Resolve()

	printer := minifier.NewMinifier(tree, resolver.IdentMap)

	return printer.Minify(), nil
}
