# lugo-min

lugo-min is a Lua code minifier built on top of the lugo AST architecture.

## Installation

Pre-built binaries are available for download on the GitHub Releases page.

## Usage

Minify a file and output to stdout:

```
lugo-min input.lua
```

Minify in-place:

```
lugo-min --write input.lua
```

Minify from stdin:

```
cat input.lua | lugo-min > output.lua
```

## Arguments

`[file...]`
Optional list of paths to Lua files to minify. If no files are provided, lugo-min reads from standard input and writes to standard output.

## Flags

All boolean flags defaulted to `true` can be disabled using the `--flag=false` syntax.

| Flag | Description | Default |
|---|---|---|
| `--write`, `-w` | Overwrite existing files in-place. | `false` |
| `--output`, `-o` | Write output to a specific file or directory. | `""` |
| `--no-rename` | Disable local variable renaming. | `false` |
| `--cache-globals` | Cache global functions and constants at the top of the file. | `true` |
| `--optimize-loops` | Refactor generic ipairs loops to numeric loops. | `true` |
| `--const-fold` | Perform compile-time optimization of static expressions. | `true` |
| `--combine-locals` | Combine consecutive local definitions into a single statement. | `true` |
| `--optimize-table-insert` | Refactor table.insert(t, v) to t[#t+1] = v. | `true` |
| `--global-threshold` | Minimum usage count before a global namespace gets cached. | `1` |
| `--max-locals` | Maximum root-scope locals after global caching; 0 disables the cap. | `150` |
| `--reserved-names` | Comma-separated list of names that should never be used as minified local names. | `[]` |
| `--no-shadow-all-globals` | Prevent renaming local variables to any standard Lua global name. | `false` |
| `--no-shadow-referenced-globals` | Prevent renaming local variables to any global name referenced in the script. | `true` |
| `--obfuscate-events` | Obfuscate FiveM event names by replacing them with short identifiers. | `true` |
| `--event-functions` | Comma-separated list of additional event functions beyond the defaults. | `[]` |
| `--shorten-numbers` | Shorten numeric literals (e.g. 0.5 to .5, 5.0 to 5). | `true` |
| `--fold-gethashkey` | Fold GetHashKey("string") calls to their JOAAT hash at compile time. | `false` |
| `--simplify-citizen` | Simplify Citizen.Wait(x) to Wait(x) and Citizen.CreateThread(f) to CreateThread(f). | `false` |
| `--fixpoint` | Run optimization passes repeatedly until no more changes are detected. | `true` |
| `--fold-string-concat` | Fold constant string concatenation ("a" .. "b" to "ab"). | `true` |
| `--fold-unary` | Fold constant unary expressions (e.g. not true to false, -(-x) to x, #"str" to length). | `true` |
| `--fold-logical` | Fold logical short-circuit expressions (e.g. true and x to x, false or x to x). | `false` |
| `--dead-code` | Eliminate dead code (unreachable statements, if false then ... end). | `false` |
| `--rename-calls` | Rename function calls (format: old=new,old2=new2). | `[]` |
| `--skip-event-strings-in` | Comma-separated list of function names where event string replacement should be skipped. | `[]` |
