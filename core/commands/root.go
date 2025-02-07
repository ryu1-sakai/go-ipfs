package commands

import (
	"io"
	"strings"

	cmds "github.com/ipfs/go-ipfs/commands"
	files "github.com/ipfs/go-ipfs/core/commands/files"
	ocmd "github.com/ipfs/go-ipfs/core/commands/object"
	unixfs "github.com/ipfs/go-ipfs/core/commands/unixfs"
	logging "gx/ipfs/Qmazh5oNUVsDZTs2g59rq8aYQqwpss8tcUWQzor5sCCEuH/go-log"
)

var log = logging.Logger("core/commands")

const (
	ApiOption = "api"
)

var Root = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Global p2p merkle-dag filesystem.",
		Synopsis: `
ipfs [<flags>] <command> [<arg>] ...
`,
		Subcommands: `
BASIC COMMANDS
  init          Initialize ipfs local configuration
  add <path>    Add a file to ipfs
  cat <ref>     Show ipfs object data
  get <ref>     Download ipfs objects
  ls <ref>      List links from an object
  refs <ref>    List hashes of links from an object

DATA STRUCTURE COMMANDS
  block         Interact with raw blocks in the datastore
  object        Interact with raw dag nodes
  files         Interact with objects as if they were a unix filesystem

ADVANCED COMMANDS
  daemon        Start a long-running daemon process
  mount         Mount an ipfs read-only mountpoint
  resolve       Resolve any type of name
  name          Publish or resolve IPNS names
  dns           Resolve DNS links
  pin           Pin objects to local storage
  repo          Manipulate the IPFS repository

NETWORK COMMANDS
  id            Show info about ipfs peers
  bootstrap     Add or remove bootstrap peers
  swarm         Manage connections to the p2p network
  dht           Query the DHT for values or peers
  ping          Measure the latency of a connection
  diag          Print diagnostics

TOOL COMMANDS
  config        Manage configuration
  version       Show ipfs version information
  update        Download and apply go-ipfs updates
  commands      List all available commands

Use 'ipfs <command> --help' to learn more about each command.

ipfs uses a repository in the local file system. By default, the repo is located
at ~/.ipfs. To change the repo location, set the $IPFS_PATH environment variable:

  export IPFS_PATH=/path/to/ipfsrepo
`,
	},
	Options: []cmds.Option{
		cmds.StringOption("config", "c", "Path to the configuration file to use."),
		cmds.BoolOption("debug", "D", "Operate in debug mode."),
		cmds.BoolOption("help", "Show the full command help text."),
		cmds.BoolOption("h", "Show a short version of the command help text."),
		cmds.BoolOption("local", "L", "Run the command locally, instead of using the daemon."),
		cmds.StringOption(ApiOption, "Use a specific API instance (defaults to /ip4/127.0.0.1/tcp/5001)"),
	},
}

// commandsDaemonCmd is the "ipfs commands" command for daemon
var CommandsDaemonCmd = CommandsCmd(Root)

var rootSubcommands = map[string]*cmds.Command{
	"add":       AddCmd,
	"block":     BlockCmd,
	"bootstrap": BootstrapCmd,
	"cat":       CatCmd,
	"commands":  CommandsDaemonCmd,
	"config":    ConfigCmd,
	"dht":       DhtCmd,
	"diag":      DiagCmd,
	"dns":       DNSCmd,
	"files":     files.FilesCmd,
	"get":       GetCmd,
	"id":        IDCmd,
	"log":       LogCmd,
	"ls":        LsCmd,
	"mount":     MountCmd,
	"name":      NameCmd,
	"object":    ocmd.ObjectCmd,
	"pin":       PinCmd,
	"ping":      PingCmd,
	"refs":      RefsCmd,
	"repo":      RepoCmd,
	"resolve":   ResolveCmd,
	"stats":     StatsCmd,
	"swarm":     SwarmCmd,
	"tar":       TarCmd,
	"tour":      tourCmd,
	"file":      unixfs.UnixFSCmd,
	"update":    ExternalBinary(),
	"version":   VersionCmd,
	"bitswap":   BitswapCmd,
}

// RootRO is the readonly version of Root
var RootRO = &cmds.Command{}

var CommandsDaemonROCmd = CommandsCmd(RootRO)

var RefsROCmd = &cmds.Command{}

var rootROSubcommands = map[string]*cmds.Command{
	"block": &cmds.Command{
		Subcommands: map[string]*cmds.Command{
			"stat": blockStatCmd,
			"get":  blockGetCmd,
		},
	},
	"cat":      CatCmd,
	"commands": CommandsDaemonROCmd,
	"dns":      DNSCmd,
	"get":      GetCmd,
	"ls":       LsCmd,
	"name": &cmds.Command{
		Subcommands: map[string]*cmds.Command{
			"resolve": IpnsCmd,
		},
	},
	"object": &cmds.Command{
		Subcommands: map[string]*cmds.Command{
			"data":  ocmd.ObjectDataCmd,
			"links": ocmd.ObjectLinksCmd,
			"get":   ocmd.ObjectGetCmd,
			"stat":  ocmd.ObjectStatCmd,
			"patch": ocmd.ObjectPatchCmd,
		},
	},
	"refs":    RefsROCmd,
	"resolve": ResolveCmd,
	"version": VersionCmd,
}

func init() {
	*RootRO = *Root

	// sanitize readonly refs command
	*RefsROCmd = *RefsCmd
	RefsROCmd.Subcommands = map[string]*cmds.Command{}

	Root.Subcommands = rootSubcommands
	RootRO.Subcommands = rootROSubcommands
}

type MessageOutput struct {
	Message string
}

func MessageTextMarshaler(res cmds.Response) (io.Reader, error) {
	return strings.NewReader(res.Output().(*MessageOutput).Message), nil
}
