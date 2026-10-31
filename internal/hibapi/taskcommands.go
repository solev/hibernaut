package hibapi

type TaskCommand struct {
	Path, Args, Help, Summary string
	E2B, Laptop               bool
	RequestID                 bool
}

func TaskCommands() []TaskCommand {
	return []TaskCommand{
		{Path: "task start", Laptop: true, RequestID: true, Summary: "start a command in its own task machine",
			Help: "save a task and its first run from the request and files on stdin. Nothing " +
				"runs until the daemon starts it"},
		{Path: "task run", Args: "TASK", RequestID: true,
			Help: "save a follow-up run of TASK on its machine the same way"},
		{Path: "task ls", E2B: true, Laptop: true, Summary: "list tasks and their latest runs",
			Help: "read tasks newest first, with each latest run and machine state"},
		{Path: "task show", Args: "TASK | -request ID", E2B: true, Laptop: true,
			Summary: "show a task or look up a request",
			Help:    "read every run, hold, request and removal of a task or submission"},
		{Path: "task logs", Args: "TASK [-run N] [-from BYTE]", Laptop: true,
			Summary: "read or follow a task run's output",
			Help:    "print the run's setup, command and finish output; -from counts bytes already read. JSON reads at most 1 MiB"},
		{Path: "task wait", Args: "TASK [-run N]", Laptop: true,
			Summary: "wait for a task run and print its outcome",
			Help:    "wait for a run to end and report its outcome"},
		{Path: "task cancel", Args: "TASK [-request-id ID]", Laptop: true, RequestID: true,
			Summary: "stop a task's live run",
			Help:    "record a request for the daemon to stop the live run"},
		{Path: "task rescue", Args: "TASK [-request-id ID]", Laptop: true, RequestID: true,
			Summary: "resume a held task machine and stop its stage",
			Help:    "record consent for the daemon to resume a held machine and kill its stage"},
		{Path: "task rm", Args: "TASK [-request-id ID]", Laptop: true, RequestID: true,
			Summary: "delete a task and its machine after asking",
			Help:    "record a request for the daemon to stop the live run and remove the task"},
		{Path: "task cleanup", Args: "[-n]",
			Help: "delete inputs and logs no task refers to"},
		{Path: "recipe put", Args: "NAME", Laptop: true, Summary: "save a named command",
			Help: "save a recipe from the JSON on stdin"},
		{Path: "recipe ls", Laptop: true, Summary: "list saved recipes", Help: "list recipes"},
		{Path: "recipe show", Args: "NAME", Laptop: true, Summary: "show a saved recipe", Help: "show a recipe"},
		{Path: "recipe rm", Args: "NAME", Laptop: true, Summary: "remove a saved recipe", Help: "remove a recipe"},
		{Path: "recipe default", Args: "[NAME]", Laptop: true, Summary: "show or set the default recipe",
			Help: "show or set the recipe hib task start uses without -- CMD or --recipe"},
	}
}
