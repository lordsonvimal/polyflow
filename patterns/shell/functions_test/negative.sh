# Negative fixture: no function_definition, and no literal (word) command
# name — every command invocation here is dynamic (variable-built), and the
# two assignments never reach a command position at all. Also proves comment
# bodies mentioning function-like text never leak into a match (rule 11):
# a real tree-sitter grammar keeps comment nodes opaque by construction, but
# the fixture still documents the expectation the way a splitter-parser
# fixture would.
#   function fake_fn { echo "should never match"; }
#   bar() { echo "should never match either"; }
FOO=bar
BAZ="qux"
$DYNAMIC_CMD --flag
"$CMD_VAR" arg
