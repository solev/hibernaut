package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/version"
)

type command struct {
	name         string
	args, opts   string
	summary      string
	about        string
	examples     []string
	subs         []*command
	parent       *command
	hidden       bool
	oneLine      bool
	nargs        int
	takesCommand bool
	raw          bool
	guard        func(args []string) error
	flags        func(fl *flag.FlagSet) runner
}

type runner func(ctx context.Context, a *app, args []string) int

const anyArgs = -1

func commandTable() *command {
	root := &command{
		name: "hib", args: "[--config DIR] COMMAND [ARG...]",
		about: "hib makes machines on your Hibernaut host and runs them, over SSH. ssh NAME.hib logs in to\n" +
			"one, waking it if it's paused.",
		examples: []string{"hib init --host azureuser@203.0.113.7", "hib new app", "ssh app.hib"},
		flags:    rootFlags,
		subs: []*command{
			{
				name: "init", opts: "--host USER@HOST [--identity FILE] [--azure-group GROUP --azure-vm VM] [--ssh-config FILE]",
				summary: "set this laptop up for a host",
				about: "Saves which host to use, checks that hibd answers there, and adds hib's settings to your ssh\n" +
					"config so that ssh NAME.hib works. With --azure-group and --azure-vm, hib starts the host's VM\n" +
					"when it's asleep. Run it again to change any of these.",
				examples: []string{
					"hib init --host azureuser@203.0.113.7",
					"hib init --host azureuser@203.0.113.7 --azure-group rg-hib --azure-vm hib",
				},
				flags: initFlags,
			},
			{
				name: "new", args: "NAME", opts: "[--image IMAGE] [--size SIZE] [--idle 15m]",
				summary: "make a machine",
				about: "Makes a machine from an image. Log in with ssh NAME.hib. The machine pauses once it has been\n" +
					"idle for a while, memory included, and logging in wakes it where it left off.",
				examples: []string{"hib new app", "hib new big --size large --idle 2h", "hib new task --image myimg"},
				nargs:    1, flags: newFlags,
			},
			{
				name: "ls", summary: "list machines, and when each pauses unless it's busy",
				about: "Lists your machines with their state, image, size and idle time, and when each running one\n" +
					"pauses unless it's busy. Ends with how many more machines the host has room for.",
				examples: []string{"hib ls"},
				flags:    jsonOnly(configured((*app).ls)),
			},
			{
				name: "rm", args: "NAME", opts: "[--yes]",
				summary: "delete a machine and its disk for good",
				about: "Deletes the machine and its disk. This can't be undone, so on a terminal it asks first.\n" +
					"--yes skips the question. Without a terminal, --yes is needed.",
				examples: []string{"hib rm app", "hib rm app --yes"},
				nargs:    1, flags: rmFlags,
			},
			{
				name: "pause", args: "NAME", summary: "pause a machine; logging in wakes it",
				about: "Pauses the machine now, memory included, instead of waiting until it's idle. ssh NAME.hib\n" +
					"wakes it where it left off.",
				examples: []string{"hib pause app"},
				nargs:    1, flags: noFlags(configured((*app).pause)),
			},
			{
				name: "fork", args: "MACHINE", opts: "[--count N]",
				summary: "copy a machine",
				about: fmt.Sprintf("Copies the machine, memory included, as MACHINE-1, MACHINE-2 and so on. Each copy gets\n"+
					"fresh SSH host keys. At most %d copies at once.\n\n", machines.MaxCopies) + snapshotHelp,
				examples: []string{"hib fork app", "hib fork app --count 3"},
				nargs:    1, flags: forkFlags,
			},
			{
				name: "ssh", args: "NAME [-- CMD]", summary: "log in to a machine, or run CMD there",
				about:    "Runs ssh NAME.hib, with CMD if you give one. It wakes the machine if it's paused.",
				examples: []string{"hib ssh app", "hib ssh app -- uptime"},
				nargs:    1, takesCommand: true, flags: noFlags(configured((*app).sshTo)),
			},
			{
				name: "exec", args: "NAME -- CMD [ARG...]", summary: "run CMD on a machine",
				about:    "Runs CMD on the machine, as ssh NAME.hib -- CMD does. It wakes the machine if it's paused.",
				examples: []string{"hib exec app -- uptime", "hib exec app -- cat /etc/os-release"},
				nargs:    1, takesCommand: true, flags: noFlags(configured((*app).exec)),
			},
			{
				name:     "connect",
				about:    "Gives every machine a login, which the host keeps. Machines get placeholders, never the tokens.",
				examples: []string{"hib connect github", "hib connect codex"},
				guard:    checkConnectArgs,
				subs: []*command{
					{
						name: "github", opts: "[--from-gh | --token]",
						summary: "give machines your GitHub login",
						about: "Shows a code and opens a browser to approve the host's GitHub login. --from-gh copies this\n" +
							"laptop's gh login instead, and --token reads a token you made from a hidden prompt or stdin.\n" +
							"Never pass a token as an argument.",
						examples: []string{"hib connect github", "hib connect github --from-gh", "hib connect github --token < token.txt"},
						flags:    connectGitHubFlags,
					},
					{
						name: "codex", summary: "give machines your Codex login on a ChatGPT plan",
						about: "Shows a code and a link to approve a Codex login with your ChatGPT plan. The host keeps the\n" +
							"tokens fresh.",
						examples: []string{"hib connect codex"},
						flags:    noFlags(configured((*app).connectCodex)),
					},
				},
			},
			{
				name: "disconnect", oneLine: true, summary: "remove a login from the host",
				about:    "Removes a login that hib connect stored. Machines keep their placeholders, which stop working.",
				examples: []string{"hib disconnect codex", "hib disconnect github"},
				subs: []*command{
					{
						name: "codex", summary: "remove the Codex login from the host",
						about: "Removes the Codex login from the host. Codex in machines stops working until hib connect\n" +
							"codex. Revoke the login itself in ChatGPT's settings.",
						examples: []string{"hib disconnect codex"},
						flags:    noFlags(unbuilt),
					},
					{
						name: "github", summary: "remove the GitHub login from the host",
						about: "Removes the GitHub login from the host. Revoke the login itself in GitHub's authorized\n" +
							"OAuth Apps, or on the token's page.",
						examples: []string{"hib disconnect github"},
						flags:    noFlags(unbuilt),
					},
				},
			},
			{
				name: "status", summary: "show the health of the host, E2B, each secret and the Codex login",
				about: "Shows whether the host, E2B, each secret and the Codex login work, and what to do when one\n" +
					"doesn't. It never wakes the host. Exits 1 when something is failing.",
				examples: []string{"hib status"},
				flags:    jsonOnly(configured((*app).status)),
			},
			{
				name: "secret",
				about: "Stores secrets on the host, bound to the sites that may get them. Machines see a placeholder,\n" +
					"which the host swaps for the real value only in requests to those sites.",
				examples: []string{"hib secret add OPENAI --env OPENAI_API_KEY --bind api.openai.com", "hib secret ls"},
				subs: []*command{
					{
						name: "add", args: "NAME", opts: "--env VAR --bind SITE[,SITE...]",
						summary: "store a bound secret",
						about: "Stores a secret, bound to the sites. Machines get a placeholder in VAR. The value comes\n" +
							"from a hidden prompt or stdin, never from an argument.",
						examples: []string{
							"hib secret add OPENAI --env OPENAI_API_KEY --bind api.openai.com",
							"hib secret add NPM --env NPM_TOKEN --bind registry.npmjs.org < npm-token.txt",
						},
						nargs: 1, flags: secretAddFlags,
					},
					{
						name: "ls", summary: "list bound secrets",
						about:    "Lists the secrets with their variables, sites and placeholders, never their values.",
						examples: []string{"hib secret ls"},
						flags:    jsonOnly(configured((*app).secretLS)),
					},
					{
						name: "rm", args: "NAME", summary: "remove a bound secret",
						about:    "Removes the secret from the host. Machines keep its placeholder, which stops working.",
						examples: []string{"hib secret rm OPENAI"},
						nargs:    1, flags: noFlags(configured((*app).secretRM)),
					},
				},
			},
			{
				name: "log", opts: "[--n N]", summary: "show secret swap audit entries",
				about:    "Shows the newest swaps of placeholders for secrets: when, which machine, the site, the method,\nthe path and the secrets.",
				examples: []string{"hib log", "hib log --n 200"},
				flags:    logFlags,
			},
			{
				name: "image",
				about: "Builds, saves, lists and removes images, which machines are made from. Built-in images come\n" +
					"with hib, and saved ones are machines you saved.",
				examples: []string{"hib image build dev", "hib image ls"},
				subs: []*command{
					{
						name: "build", args: "NAME", opts: "[--force] [--verbose]",
						summary: "build an image on the host, in every size",
						about: "Builds the built-in image NAME on the host, in every size: medium first, then small and\n" +
							"large. E2B reuses the steps that haven't changed.",
						examples: []string{"hib image build dev", "hib image build dev --force"},
						nargs:    1, flags: imageBuildFlags,
					},
					{
						name: "save", args: "MACHINE NAME", summary: "save a machine as an image, memory included",
						about:    "Saves the machine as the image NAME, memory included. Machines made from it start where\nMACHINE was.\n\n" + snapshotHelp,
						examples: []string{"hib image save app myimg"},
						nargs:    2, flags: noFlags(configured((*app).imageSave)),
					},
					{
						name: "ls", summary: "list built-in and saved images",
						about:    "Lists the built-in and saved images, with their memory and when they were made.",
						examples: []string{"hib image ls"},
						flags:    jsonOnly(configured((*app).imageList)),
					},
					{
						name: "rm", args: "NAME", summary: "remove a saved image no machine uses",
						about:    "Removes a saved image. It refuses while a machine made from it is running or paused.",
						examples: []string{"hib image rm myimg"},
						nargs:    1, flags: noFlags(configured((*app).imageRemove)),
					},
				},
			},
			{
				name: "completion", oneLine: true, summary: "print a shell completion script",
				about:    "Prints a script that completes hib's commands, flags, sizes, and machine and image names.",
				examples: []string{"source <(hib completion bash)", "source <(hib completion zsh)", "hib completion fish | source"},
				subs: []*command{
					{
						name: "bash", summary: "print a bash completion script",
						about: "Prints a bash completion script.", examples: []string{"source <(hib completion bash)"},
						flags: noFlags(unbuilt),
					},
					{
						name: "zsh", summary: "print a zsh completion script",
						about: "Prints a zsh completion script.", examples: []string{"source <(hib completion zsh)"},
						flags: noFlags(unbuilt),
					},
					{
						name: "fish", summary: "print a fish completion script",
						about: "Prints a fish completion script.", examples: []string{"hib completion fish | source"},
						flags: noFlags(unbuilt),
					},
				},
			},
			{
				name: "__complete", args: "WORD...", hidden: true, raw: true,
				summary:  "complete a command line",
				about:    "Prints what completes the last WORD. The completion scripts run it.",
				examples: []string{"hib __complete new app --size ''"},
				nargs:    anyArgs, flags: noFlags(unbuilt),
			},
			{
				name: "bridge", args: "NAME.hib", summary: "connect to a machine's SSH server; ssh runs this for you",
				about: "Carries an SSH connection to the machine, waking the host and the machine first. hib init sets\n" +
					"ssh up to run it for NAME.hib, so you don't run it yourself.",
				examples: []string{"ssh app.hib"},
				nargs:    1, flags: noFlags(configured((*app).bridgeCmd)),
			},
			{
				name: "version", summary: "print the version", about: "Prints hib's version.",
				examples: []string{"hib version"},
				flags:    noFlags(printVersion),
			},
			{
				name: "help", args: "[COMMAND [SUB]]", summary: "show help for hib or a command",
				about:    "Shows a command's synopsis, what it does, its flags and examples. Without one, lists the commands.",
				examples: []string{"hib help new", "hib help image build"},
				nargs:    anyArgs, flags: noFlags(help),
			},
		},
	}
	link(root)
	return root
}

func link(c *command) {
	for _, sub := range c.subs {
		sub.parent = c
		link(sub)
	}
}

func (c *command) path() string {
	if c.parent == nil {
		return ""
	}
	return strings.TrimSpace(c.parent.path() + " " + c.name)
}

func (c *command) title() string {
	return strings.TrimSpace("hib " + c.path())
}

func (c *command) root() *command {
	for c.parent != nil {
		c = c.parent
	}
	return c
}

func (c *command) synopsis() string {
	return joinWords(c.title(), c.args, c.opts, c.choices())
}

func (c *command) choices() string {
	if c.subs == nil || c.parent == nil {
		return ""
	}
	return strings.Join(c.names(), "|")
}

func joinWords(words ...string) string {
	var parts []string
	for _, w := range words {
		if w != "" {
			parts = append(parts, w)
		}
	}
	return strings.Join(parts, " ")
}

func (c *command) names() []string {
	var names []string
	for _, sub := range c.subs {
		if !sub.hidden {
			names = append(names, sub.name)
		}
	}
	return names
}

func (c *command) sub(name string) *command {
	for _, sub := range c.subs {
		if sub.name == name {
			return sub
		}
	}
	return nil
}

func noFlags(run runner) func(*flag.FlagSet) runner {
	return func(*flag.FlagSet) runner { return run }
}

func jsonOnly(run runner) func(*flag.FlagSet) runner {
	return func(fl *flag.FlagSet) runner {
		jsonFlag(fl)
		return run
	}
}

func jsonFlag(fl *flag.FlagSet) *bool {
	return fl.Bool("json", false, "print one JSON object instead of text")
}

func configured(run func(a *app, ctx context.Context, cfg *config, args []string) int) runner {
	return func(ctx context.Context, a *app, args []string) int {
		cfg, err := a.settings()
		if err != nil {
			return a.fail(err)
		}
		return run(a, ctx, cfg, args)
	}
}

func unbuilt(_ context.Context, a *app, _ []string) int {
	fmt.Fprintf(a.stderr, "%s isn't built yet\n", a.command.title())
	return 1
}

func printVersion(_ context.Context, a *app, _ []string) int {
	fmt.Fprintln(a.stdout, "hib", version.Version)
	return 0
}

func help(_ context.Context, a *app, args []string) int {
	c := a.command.root()
	for _, name := range args {
		if c.subs == nil {
			return a.mistake("unexpected argument " + name)
		}
		sub := c.sub(name)
		if sub == nil {
			return a.mistake(unknownCommand(name, c))
		}
		c = sub
	}
	printHelp(a.stdout, c)
	return 0
}

func rootFlags(fl *flag.FlagSet) runner {
	dir := fl.String("config", "", "the settings `dir`ectory (default ~/.config/hibernaut)")
	showVersion := fl.Bool("version", false, "print the version")
	return func(ctx context.Context, a *app, args []string) int {
		a.configFlag = *dir
		if *showVersion {
			return printVersion(ctx, a, nil)
		}
		if len(args) == 0 {
			printHelp(a.stderr, a.command)
			return 2
		}
		return a.runSub(ctx, a.command, args[0], args[1:])
	}
}

func newFlags(fl *flag.FlagSet) runner {
	image := fl.String("image", images.Default, "the `image` to make the machine from")
	size := fl.String("size", "", "small, medium or large (default medium, or the saved image's `size`)")
	idle := fl.Duration("idle", 0, "pause the machine after this long idle, such as 1h (default 15m, and 0 means never)")
	jsonFlag(fl)
	return configured(func(a *app, ctx context.Context, cfg *config, args []string) int {
		return a.newMachine(ctx, cfg, fl, args[0], *image, *size, *idle)
	})
}

func rmFlags(fl *flag.FlagSet) runner {
	yes := fl.Bool("yes", false, "delete without asking")
	return configured(func(a *app, ctx context.Context, cfg *config, args []string) int {
		return a.rm(ctx, cfg, args[0], *yes)
	})
}

func forkFlags(fl *flag.FlagSet) runner {
	count := fl.Int("count", 1, fmt.Sprintf("make `N` copies, from 1 to %d", machines.MaxCopies))
	jsonFlag(fl)
	return configured(func(a *app, ctx context.Context, cfg *config, args []string) int {
		return a.fork(ctx, cfg, args[0], *count)
	})
}

func connectGitHubFlags(fl *flag.FlagSet) runner {
	fromGH := fl.Bool("from-gh", false, "copy this laptop's gh login")
	token := fl.Bool("token", false, "read a token from a hidden prompt or stdin")
	return configured(func(a *app, ctx context.Context, cfg *config, _ []string) int {
		return a.connectGitHub(ctx, cfg, *fromGH, *token)
	})
}

func secretAddFlags(fl *flag.FlagSet) runner {
	env := fl.String("env", "", "the environment `var`iable that holds the placeholder in machines")
	bind := fl.String("bind", "", "the `site`s that get the real value, separated by commas")
	return configured(func(a *app, ctx context.Context, cfg *config, args []string) int {
		return a.secretAdd(ctx, cfg, args[0], *env, *bind)
	})
}

func logFlags(fl *flag.FlagSet) runner {
	n := fl.Int("n", 50, "show the `N` newest entries")
	jsonFlag(fl)
	return configured(func(a *app, ctx context.Context, cfg *config, _ []string) int {
		return a.log(ctx, cfg, *n)
	})
}

func imageBuildFlags(fl *flag.FlagSet) runner {
	force := fl.Bool("force", false, "rebuild every step, not only those that changed")
	fl.Bool("verbose", false, "print E2B's full build log")
	return configured(func(a *app, ctx context.Context, cfg *config, args []string) int {
		return a.imageBuild(ctx, cfg, args[0], *force)
	})
}
