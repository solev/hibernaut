package main

import (
	"context"
	"fmt"
)

const bashCompletion = `# bash completion for hib. Load it with: source <(hib completion bash)
_hib() {
	local candidate
	COMPREPLY=()
	while IFS= read -r candidate; do
		COMPREPLY+=("$candidate")
	done < <(command hib __complete "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null)
}
complete -F _hib hib
`

const zshCompletion = `#compdef hib
# zsh completion for hib. Load it with: source <(hib completion zsh)
_hib() {
	local -a candidates
	candidates=("${(@f)$(command hib __complete "${(@)words[2,CURRENT]}" 2>/dev/null)}")
	candidates=("${(@)candidates:#}")
	compadd -- "${(@)candidates}"
}
if [[ $zsh_eval_context[-1] == loadautofunc ]]; then
	_hib "$@"
else
	compdef _hib hib
fi
`

const fishCompletion = `# fish completion for hib. Load it with: hib completion fish | source
function __hib_complete
	set -l tokens (commandline -opc)
	set -e tokens[1]
	set -l current (commandline -ct)
	command hib __complete $tokens "$current" 2>/dev/null
end
complete -c hib -f -a '(__hib_complete)'
`

func printScript(script string) runner {
	return func(_ context.Context, a *app, _ []string) int {
		fmt.Fprint(a.stdout, script)
		return 0
	}
}
