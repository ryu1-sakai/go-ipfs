// cmd/ipfs implements the primary CLI binary for ipfs
package main

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"syscall"
	"time"

	manet "gx/ipfs/QmTrxSBY8Wqd5aBB4MeizeSzS5xFbK8dQBrYaMsiGnCBhb/go-multiaddr-net"
	ma "gx/ipfs/QmcobAGsCjYt5DXoq9et9L8yR8er7o7Cu3DTvpaq12jYSz/go-multiaddr"

	cmds "github.com/ipfs/go-ipfs/commands"
	cmdsCli "github.com/ipfs/go-ipfs/commands/cli"
	cmdsHttp "github.com/ipfs/go-ipfs/commands/http"
	core "github.com/ipfs/go-ipfs/core"
	coreCmds "github.com/ipfs/go-ipfs/core/commands"
	repo "github.com/ipfs/go-ipfs/repo"
	config "github.com/ipfs/go-ipfs/repo/config"
	fsrepo "github.com/ipfs/go-ipfs/repo/fsrepo"
	u "gx/ipfs/QmZNVWh8LLjAavuQ2JXuFmuYH3C11xo988vSgp7UQrTRj1/go-ipfs-util"
	context "gx/ipfs/QmZy2y8t9zQH2a1b8q2ZSLKp17ATuJoCNxxyMFG5qFExpt/go-net/context"
	logging "gx/ipfs/Qmazh5oNUVsDZTs2g59rq8aYQqwpss8tcUWQzor5sCCEuH/go-log"
)

// log is the command logger
var log = logging.Logger("cmd/ipfs")

var (
	errUnexpectedApiOutput = errors.New("api returned unexpected output")
	errApiVersionMismatch  = errors.New("api version mismatch")
	errRequestCanceled     = errors.New("request canceled")
)

const (
	EnvEnableProfiling = "IPFS_PROF"
	cpuProfile         = "ipfs.cpuprof"
	heapProfile        = "ipfs.memprof"
	errorFormat        = "ERROR: %v\n\n"
)

type cmdInvocation struct {
	path []string
	cmd  *cmds.Command
	req  cmds.Request
	node *core.IpfsNode
}

// main roadmap:
// - parse the commandline to get a cmdInvocation
// - if user requests help, print it and exit.
// - run the command invocation
// - output the response
// - if anything fails, print error, maybe with help
func main() {
	rand.Seed(time.Now().UnixNano())
	runtime.GOMAXPROCS(3) // FIXME rm arbitrary choice for n
	ctx := logging.ContextWithLoggable(context.Background(), logging.Uuid("session"))
	var err error
	var invoc cmdInvocation
	defer invoc.close()

	// we'll call this local helper to output errors.
	// this is so we control how to print errors in one place.
	printErr := func(err error) {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
	}

	stopFunc, err := profileIfEnabled()
	if err != nil {
		printErr(err)
		os.Exit(1)
	}
	defer stopFunc() // to be executed as late as possible

	// this is a local helper to print out help text.
	// there's some considerations that this makes easier.
	printHelp := func(long bool, w io.Writer) {
		helpFunc := cmdsCli.ShortHelp
		if long {
			helpFunc = cmdsCli.LongHelp
		}

		helpFunc("ipfs", Root, invoc.path, w)
	}

	// this is a message to tell the user how to get the help text
	printMetaHelp := func(w io.Writer) {
		cmdPath := strings.Join(invoc.path, " ")
		fmt.Fprintf(w, "Use 'ipfs %s --help' for information about this command\n", cmdPath)
	}

	// Handle `ipfs help'
	if len(os.Args) == 2 && os.Args[1] == "help" {
		printHelp(false, os.Stdout)
		os.Exit(0)
	}

	// parse the commandline into a command invocation
	parseErr := invoc.Parse(ctx, os.Args[1:])

	// BEFORE handling the parse error, if we have enough information
	// AND the user requested help, print it out and exit
	if invoc.req != nil {
		longH, shortH, err := invoc.requestedHelp()
		if err != nil {
			printErr(err)
			os.Exit(1)
		}
		if longH || shortH {
			printHelp(longH, os.Stdout)
			os.Exit(0)
		}
	}

	// ok now handle parse error (which means cli input was wrong,
	// e.g. incorrect number of args, or nonexistent subcommand)
	if parseErr != nil {
		printErr(parseErr)

		// this was a user error, print help.
		if invoc.cmd != nil {
			// we need a newline space.
			fmt.Fprintf(os.Stderr, "\n")
			printHelp(false, os.Stderr)
		}
		os.Exit(1)
	}

	// here we handle the cases where
	// - commands with no Run func are invoked directly.
	// - the main command is invoked.
	if invoc.cmd == nil || invoc.cmd.Run == nil {
		printHelp(false, os.Stdout)
		os.Exit(0)
	}

	// ok, finally, run the command invocation.
	intrh, ctx := invoc.SetupInterruptHandler(ctx)
	defer intrh.Close()

	output, err := invoc.Run(ctx)
	if err != nil {
		printErr(err)

		// if this error was a client error, print short help too.
		if isClientError(err) {
			printMetaHelp(os.Stderr)
		}
		os.Exit(1)
	}

	// everything went better than expected :)
	_, err = io.Copy(os.Stdout, output)
	if err != nil {
		printErr(err)

		os.Exit(1)
	}
}

func (i *cmdInvocation) Run(ctx context.Context) (output io.Reader, err error) {

	// check if user wants to debug. option OR env var.
	debug, _, err := i.req.Option("debug").Bool()
	if err != nil {
		return nil, err
	}
	if debug || os.Getenv("IPFS_LOGGING") == "debug" {
		u.Debug = true
		logging.SetDebugLogging()
	}
	if u.GetenvBool("DEBUG") {
		u.Debug = true
	}

	res, err := callCommand(ctx, i.req, Root, i.cmd)
	if err != nil {
		return nil, err
	}

	if err := res.Error(); err != nil {
		return nil, err
	}

	return res.Reader()
}

func (i *cmdInvocation) constructNodeFunc(ctx context.Context) func() (*core.IpfsNode, error) {
	return func() (*core.IpfsNode, error) {
		if i.req == nil {
			return nil, errors.New("constructing node without a request")
		}

		cmdctx := i.req.InvocContext()
		if cmdctx == nil {
			return nil, errors.New("constructing node without a request context")
		}

		r, err := fsrepo.Open(i.req.InvocContext().ConfigRoot)
		if err != nil { // repo is owned by the node
			return nil, err
		}

		// ok everything is good. set it on the invocation (for ownership)
		// and return it.
		n, err := core.NewNode(ctx, &core.BuildCfg{
			Online: cmdctx.Online,
			Repo:   r,
		})
		if err != nil {
			return nil, err
		}
		i.node = n
		return i.node, nil
	}
}

func (i *cmdInvocation) close() {
	// let's not forget teardown. If a node was initialized, we must close it.
	// Note that this means the underlying req.Context().Node variable is exposed.
	// this is gross, and should be changed when we extract out the exec Context.
	if i.node != nil {
		log.Info("Shutting down node...")
		i.node.Close()
	}
}

func (i *cmdInvocation) Parse(ctx context.Context, args []string) error {
	var err error

	i.req, i.cmd, i.path, err = cmdsCli.Parse(args, os.Stdin, Root)
	if err != nil {
		return err
	}

	repoPath, err := getRepoPath(i.req)
	if err != nil {
		return err
	}
	log.Debugf("config path is %s", repoPath)

	// this sets up the function that will initialize the config lazily.
	cmdctx := i.req.InvocContext()
	cmdctx.ConfigRoot = repoPath
	cmdctx.LoadConfig = loadConfig
	// this sets up the function that will initialize the node
	// this is so that we can construct the node lazily.
	cmdctx.ConstructNode = i.constructNodeFunc(ctx)

	// if no encoding was specified by user, default to plaintext encoding
	// (if command doesn't support plaintext, use JSON instead)
	if !i.req.Option("encoding").Found() {
		if i.req.Command().Marshalers != nil && i.req.Command().Marshalers[cmds.Text] != nil {
			i.req.SetOption("encoding", cmds.Text)
		} else {
			i.req.SetOption("encoding", cmds.JSON)
		}
	}

	return nil
}

func (i *cmdInvocation) requestedHelp() (short bool, long bool, err error) {
	longHelp, _, err := i.req.Option("help").Bool()
	if err != nil {
		return false, false, err
	}
	shortHelp, _, err := i.req.Option("h").Bool()
	if err != nil {
		return false, false, err
	}
	return longHelp, shortHelp, nil
}

func callPreCommandHooks(ctx context.Context, details cmdDetails, req cmds.Request, root *cmds.Command) error {

	log.Event(ctx, "callPreCommandHooks", &details)
	log.Debug("Calling pre-command hooks...")

	return nil
}

func callCommand(ctx context.Context, req cmds.Request, root *cmds.Command, cmd *cmds.Command) (cmds.Response, error) {
	log.Info(config.EnvDir, " ", req.InvocContext().ConfigRoot)
	var res cmds.Response

	err := req.SetRootContext(ctx)
	if err != nil {
		return nil, err
	}

	details, err := commandDetails(req.Path(), root)
	if err != nil {
		return nil, err
	}

	client, err := commandShouldRunOnDaemon(*details, req, root)
	if err != nil {
		return nil, err
	}

	err = callPreCommandHooks(ctx, *details, req, root)
	if err != nil {
		return nil, err
	}

	if cmd.PreRun != nil {
		err = cmd.PreRun(req)
		if err != nil {
			return nil, err
		}
	}

	if client != nil && !cmd.External {
		log.Debug("Executing command via API")
		res, err = client.Send(req)
		if err != nil {
			if isConnRefused(err) {
				err = repo.ErrApiNotRunning
			}
			return nil, wrapContextCanceled(err)
		}

	} else {
		log.Debug("Executing command locally")

		err := req.SetRootContext(ctx)
		if err != nil {
			return nil, err
		}

		// Okay!!!!! NOW we can call the command.
		res = root.Call(req)

	}

	if cmd.PostRun != nil {
		cmd.PostRun(req, res)
	}

	return res, nil
}

// commandDetails returns a command's details for the command given by |path|
// within the |root| command tree.
//
// Returns an error if the command is not found in the Command tree.
func commandDetails(path []string, root *cmds.Command) (*cmdDetails, error) {
	var details cmdDetails
	// find the last command in path that has a cmdDetailsMap entry
	cmd := root
	for _, cmp := range path {
		var found bool
		cmd, found = cmd.Subcommands[cmp]
		if !found {
			return nil, fmt.Errorf("subcommand %s should be in root", cmp)
		}

		if cmdDetails, found := cmdDetailsMap[cmd]; found {
			details = cmdDetails
		}
	}
	return &details, nil
}

// commandShouldRunOnDaemon determines, from commmand details, whether a
// command ought to be executed on an IPFS daemon.
//
// It returns a client if the command should be executed on a daemon and nil if
// it should be executed on a client. It returns an error if the command must
// NOT be executed on either.
func commandShouldRunOnDaemon(details cmdDetails, req cmds.Request, root *cmds.Command) (cmdsHttp.Client, error) {
	path := req.Path()
	// root command.
	if len(path) < 1 {
		return nil, nil
	}

	if details.cannotRunOnClient && details.cannotRunOnDaemon {
		return nil, fmt.Errorf("command disabled: %s", path[0])
	}

	if details.doesNotUseRepo && details.canRunOnClient() {
		return nil, nil
	}

	// at this point need to know whether api is running. we defer
	// to this point so that we dont check unnecessarily

	// did user specify an api to use for this command?
	apiAddrStr, _, err := req.Option(coreCmds.ApiOption).String()
	if err != nil {
		return nil, err
	}

	client, err := getApiClient(req.InvocContext().ConfigRoot, apiAddrStr)
	if err == repo.ErrApiNotRunning {
		if apiAddrStr != "" && req.Command() != daemonCmd {
			// if user SPECIFIED an api, and this cmd is not daemon
			// we MUST use it. so error out.
			return nil, err
		}

		// ok for api not to be running
	} else if err != nil { // some other api error
		return nil, err
	}

	if client != nil {
		if details.cannotRunOnDaemon {
			// check if daemon locked. legacy error text, for now.
			log.Debugf("Command cannot run on daemon. Checking if daemon is locked")
			if daemonLocked, _ := fsrepo.LockedByOtherProcess(req.InvocContext().ConfigRoot); daemonLocked {
				return nil, cmds.ClientError("ipfs daemon is running. please stop it to run this command")
			}
			return nil, nil
		}

		return client, nil
	}

	if details.cannotRunOnClient {
		return nil, cmds.ClientError("must run on the ipfs daemon")
	}

	return nil, nil
}

func isClientError(err error) bool {

	// Somewhat suprisingly, the pointer cast fails to recognize commands.Error
	// passed as values, so we check both.

	// cast to cmds.Error
	switch e := err.(type) {
	case *cmds.Error:
		return e.Code == cmds.ErrClient
	case cmds.Error:
		return e.Code == cmds.ErrClient
	}
	return false
}

func getRepoPath(req cmds.Request) (string, error) {
	repoOpt, found, err := req.Option("config").String()
	if err != nil {
		return "", err
	}
	if found && repoOpt != "" {
		return repoOpt, nil
	}

	repoPath, err := fsrepo.BestKnownPath()
	if err != nil {
		return "", err
	}
	return repoPath, nil
}

func loadConfig(path string) (*config.Config, error) {
	return fsrepo.ConfigAt(path)
}

// startProfiling begins CPU profiling and returns a `stop` function to be
// executed as late as possible. The stop function captures the memprofile.
func startProfiling() (func(), error) {

	// start CPU profiling as early as possible
	ofi, err := os.Create(cpuProfile)
	if err != nil {
		return nil, err
	}
	pprof.StartCPUProfile(ofi)
	go func() {
		for _ = range time.NewTicker(time.Second * 30).C {
			err := writeHeapProfileToFile()
			if err != nil {
				log.Error(err)
			}
		}
	}()

	stopProfiling := func() {
		pprof.StopCPUProfile()
		defer ofi.Close() // captured by the closure
	}
	return stopProfiling, nil
}

func writeHeapProfileToFile() error {
	mprof, err := os.Create(heapProfile)
	if err != nil {
		return err
	}
	defer mprof.Close() // _after_ writing the heap profile
	return pprof.WriteHeapProfile(mprof)
}

// IntrHandler helps set up an interrupt handler that can
// be cleanly shut down through the io.Closer interface.
type IntrHandler struct {
	sig chan os.Signal
	wg  sync.WaitGroup
}

func NewIntrHandler() *IntrHandler {
	ih := &IntrHandler{}
	ih.sig = make(chan os.Signal, 1)
	return ih
}

func (ih *IntrHandler) Close() error {
	close(ih.sig)
	ih.wg.Wait()
	return nil
}

// Handle starts handling the given signals, and will call the handler
// callback function each time a signal is catched. The function is passed
// the number of times the handler has been triggered in total, as
// well as the handler itself, so that the handling logic can use the
// handler's wait group to ensure clean shutdown when Close() is called.
func (ih *IntrHandler) Handle(handler func(count int, ih *IntrHandler), sigs ...os.Signal) {
	signal.Notify(ih.sig, sigs...)
	ih.wg.Add(1)
	go func() {
		defer ih.wg.Done()
		count := 0
		for _ = range ih.sig {
			count++
			handler(count, ih)
		}
		signal.Stop(ih.sig)
	}()
}

func (i *cmdInvocation) SetupInterruptHandler(ctx context.Context) (io.Closer, context.Context) {

	intrh := NewIntrHandler()
	ctx, cancelFunc := context.WithCancel(ctx)

	handlerFunc := func(count int, ih *IntrHandler) {
		switch count {
		case 1:
			fmt.Println() // Prevent un-terminated ^C character in terminal

			ih.wg.Add(1)
			go func() {
				defer ih.wg.Done()
				cancelFunc()
			}()

		default:
			fmt.Println("Received another interrupt before graceful shutdown, terminating...")
			os.Exit(-1)
		}
	}

	intrh.Handle(handlerFunc, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)

	return intrh, ctx
}

func profileIfEnabled() (func(), error) {
	// FIXME this is a temporary hack so profiling of asynchronous operations
	// works as intended.
	if os.Getenv(EnvEnableProfiling) != "" {
		stopProfilingFunc, err := startProfiling() // TODO maybe change this to its own option... profiling makes it slower.
		if err != nil {
			return nil, err
		}
		return stopProfilingFunc, nil
	}
	return func() {}, nil
}

// getApiClient checks the repo, and the given options, checking for
// a running API service. if there is one, it returns a client.
// otherwise, it returns errApiNotRunning, or another error.
func getApiClient(repoPath, apiAddrStr string) (cmdsHttp.Client, error) {

	if apiAddrStr == "" {
		var err error
		if apiAddrStr, err = fsrepo.APIAddr(repoPath); err != nil {
			return nil, err
		}
	}

	addr, err := ma.NewMultiaddr(apiAddrStr)
	if err != nil {
		return nil, err
	}

	return apiClientForAddr(addr)
}

func apiClientForAddr(addr ma.Multiaddr) (cmdsHttp.Client, error) {
	_, host, err := manet.DialArgs(addr)
	if err != nil {
		return nil, err
	}

	return cmdsHttp.NewClient(host), nil
}

func isConnRefused(err error) bool {
	// unwrap url errors from http calls
	if urlerr, ok := err.(*url.Error); ok {
		err = urlerr.Err
	}

	netoperr, ok := err.(*net.OpError)
	if !ok {
		return false
	}

	return netoperr.Op == "dial"
}

func wrapContextCanceled(err error) error {
	if strings.Contains(err.Error(), "request canceled") {
		err = errRequestCanceled
	}
	return err
}
