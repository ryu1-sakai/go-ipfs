// package decision implements the decision engine for the bitswap service.
package decision

import (
	"sync"

	blocks "github.com/ipfs/go-ipfs/blocks"
	bstore "github.com/ipfs/go-ipfs/blocks/blockstore"
	bsmsg "github.com/ipfs/go-ipfs/exchange/bitswap/message"
	wl "github.com/ipfs/go-ipfs/exchange/bitswap/wantlist"
	peer "gx/ipfs/QmZwZjMVGss5rqYsJVGy18gNbkTJffFyq2x1uJ4e4p3ZAt/go-libp2p-peer"
	context "gx/ipfs/QmZy2y8t9zQH2a1b8q2ZSLKp17ATuJoCNxxyMFG5qFExpt/go-net/context"
	logging "gx/ipfs/Qmazh5oNUVsDZTs2g59rq8aYQqwpss8tcUWQzor5sCCEuH/go-log"
)

// TODO consider taking responsibility for other types of requests. For
// example, there could be a |cancelQueue| for all of the cancellation
// messages that need to go out. There could also be a |wantlistQueue| for
// the local peer's wantlists. Alternatively, these could all be bundled
// into a single, intelligent global queue that efficiently
// batches/combines and takes all of these into consideration.
//
// Right now, messages go onto the network for four reasons:
// 1. an initial `sendwantlist` message to a provider of the first key in a
//    request
// 2. a periodic full sweep of `sendwantlist` messages to all providers
// 3. upon receipt of blocks, a `cancel` message to all peers
// 4. draining the priority queue of `blockrequests` from peers
//
// Presently, only `blockrequests` are handled by the decision engine.
// However, there is an opportunity to give it more responsibility! If the
// decision engine is given responsibility for all of the others, it can
// intelligently decide how to combine requests efficiently.
//
// Some examples of what would be possible:
//
// * when sending out the wantlists, include `cancel` requests
// * when handling `blockrequests`, include `sendwantlist` and `cancel` as
//   appropriate
// * when handling `cancel`, if we recently received a wanted block from a
//   peer, include a partial wantlist that contains a few other high priority
//   blocks
//
// In a sense, if we treat the decision engine as a black box, it could do
// whatever it sees fit to produce desired outcomes (get wanted keys
// quickly, maintain good relationships with peers, etc).

var log = logging.Logger("engine")

const (
	// outboxChanBuffer must be 0 to prevent stale messages from being sent
	outboxChanBuffer = 0
)

// Envelope contains a message for a Peer
type Envelope struct {
	// Peer is the intended recipient
	Peer peer.ID

	// Block is the payload
	Block *blocks.Block

	// A callback to notify the decision queue that the task is complete
	Sent func()
}

type Engine struct {
	// peerRequestQueue is a priority queue of requests received from peers.
	// Requests are popped from the queue, packaged up, and placed in the
	// outbox.
	peerRequestQueue peerRequestQueue

	// FIXME it's a bit odd for the client and the worker to both share memory
	// (both modify the peerRequestQueue) and also to communicate over the
	// workSignal channel. consider sending requests over the channel and
	// allowing the worker to have exclusive access to the peerRequestQueue. In
	// that case, no lock would be required.
	workSignal chan struct{}

	// outbox contains outgoing messages to peers. This is owned by the
	// taskWorker goroutine
	outbox chan (<-chan *Envelope)

	bs bstore.Blockstore

	lock sync.Mutex // protects the fields immediatly below
	// ledgerMap lists Ledgers by their Partner key.
	ledgerMap map[peer.ID]*ledger
}

func NewEngine(ctx context.Context, bs bstore.Blockstore) *Engine {
	e := &Engine{
		ledgerMap:        make(map[peer.ID]*ledger),
		bs:               bs,
		peerRequestQueue: newPRQ(),
		outbox:           make(chan (<-chan *Envelope), outboxChanBuffer),
		workSignal:       make(chan struct{}, 1),
	}
	go e.taskWorker(ctx)
	return e
}

func (e *Engine) WantlistForPeer(p peer.ID) (out []wl.Entry) {
	e.lock.Lock()
	partner, ok := e.ledgerMap[p]
	if ok {
		out = partner.wantList.SortedEntries()
	}
	e.lock.Unlock()
	return out
}

func (e *Engine) taskWorker(ctx context.Context) {
	defer close(e.outbox) // because taskWorker uses the channel exclusively
	for {
		oneTimeUse := make(chan *Envelope, 1) // buffer to prevent blocking
		select {
		case <-ctx.Done():
			return
		case e.outbox <- oneTimeUse:
		}
		// receiver is ready for an outoing envelope. let's prepare one. first,
		// we must acquire a task from the PQ...
		envelope, err := e.nextEnvelope(ctx)
		if err != nil {
			close(oneTimeUse)
			return // ctx cancelled
		}
		oneTimeUse <- envelope // buffered. won't block
		close(oneTimeUse)
	}
}

// nextEnvelope runs in the taskWorker goroutine. Returns an error if the
// context is cancelled before the next Envelope can be created.
func (e *Engine) nextEnvelope(ctx context.Context) (*Envelope, error) {
	for {
		nextTask := e.peerRequestQueue.Pop()
		for nextTask == nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-e.workSignal:
				nextTask = e.peerRequestQueue.Pop()
			}
		}

		// with a task in hand, we're ready to prepare the envelope...

		block, err := e.bs.Get(nextTask.Entry.Key)
		if err != nil {
			// If we don't have the block, don't hold that against the peer
			// make sure to update that the task has been 'completed'
			nextTask.Done()
			continue
		}

		return &Envelope{
			Peer:  nextTask.Target,
			Block: block,
			Sent: func() {
				nextTask.Done()
				select {
				case e.workSignal <- struct{}{}:
					// work completing may mean that our queue will provide new
					// work to be done.
				default:
				}
			},
		}, nil
	}
}

// Outbox returns a channel of one-time use Envelope channels.
func (e *Engine) Outbox() <-chan (<-chan *Envelope) {
	return e.outbox
}

// Returns a slice of Peers with whom the local node has active sessions
func (e *Engine) Peers() []peer.ID {
	e.lock.Lock()
	defer e.lock.Unlock()

	response := make([]peer.ID, 0)
	for _, ledger := range e.ledgerMap {
		response = append(response, ledger.Partner)
	}
	return response
}

// MessageReceived performs book-keeping. Returns error if passed invalid
// arguments.
func (e *Engine) MessageReceived(p peer.ID, m bsmsg.BitSwapMessage) error {
	e.lock.Lock()
	defer e.lock.Unlock()

	if len(m.Wantlist()) == 0 && len(m.Blocks()) == 0 {
		log.Debugf("received empty message from %s", p)
	}

	newWorkExists := false
	defer func() {
		if newWorkExists {
			e.signalNewWork()
		}
	}()

	l := e.findOrCreate(p)
	if m.Full() {
		l.wantList = wl.New()
	}

	for _, entry := range m.Wantlist() {
		if entry.Cancel {
			log.Debugf("cancel %s", entry.Key)
			l.CancelWant(entry.Key)
			e.peerRequestQueue.Remove(entry.Key, p)
		} else {
			log.Debugf("wants %s - %d", entry.Key, entry.Priority)
			l.Wants(entry.Key, entry.Priority)
			if exists, err := e.bs.Has(entry.Key); err == nil && exists {
				e.peerRequestQueue.Push(entry.Entry, p)
				newWorkExists = true
			}
		}
	}

	for _, block := range m.Blocks() {
		log.Debugf("got block %s %d bytes", block.Key(), len(block.Data))
		l.ReceivedBytes(len(block.Data))
	}
	return nil
}

func (e *Engine) addBlock(block *blocks.Block) {
	work := false

	for _, l := range e.ledgerMap {
		if entry, ok := l.WantListContains(block.Key()); ok {
			e.peerRequestQueue.Push(entry, l.Partner)
			work = true
		}
	}

	if work {
		e.signalNewWork()
	}
}

func (e *Engine) AddBlock(block *blocks.Block) {
	e.lock.Lock()
	defer e.lock.Unlock()

	e.addBlock(block)
}

// TODO add contents of m.WantList() to my local wantlist? NB: could introduce
// race conditions where I send a message, but MessageSent gets handled after
// MessageReceived. The information in the local wantlist could become
// inconsistent. Would need to ensure that Sends and acknowledgement of the
// send happen atomically

func (e *Engine) MessageSent(p peer.ID, m bsmsg.BitSwapMessage) error {
	e.lock.Lock()
	defer e.lock.Unlock()

	l := e.findOrCreate(p)
	for _, block := range m.Blocks() {
		l.SentBytes(len(block.Data))
		l.wantList.Remove(block.Key())
		e.peerRequestQueue.Remove(block.Key(), p)
	}

	return nil
}

func (e *Engine) PeerDisconnected(p peer.ID) {
	// TODO: release ledger
}

func (e *Engine) numBytesSentTo(p peer.ID) uint64 {
	// NB not threadsafe
	return e.findOrCreate(p).Accounting.BytesSent
}

func (e *Engine) numBytesReceivedFrom(p peer.ID) uint64 {
	// NB not threadsafe
	return e.findOrCreate(p).Accounting.BytesRecv
}

// ledger lazily instantiates a ledger
func (e *Engine) findOrCreate(p peer.ID) *ledger {
	l, ok := e.ledgerMap[p]
	if !ok {
		l = newLedger(p)
		e.ledgerMap[p] = l
	}
	return l
}

func (e *Engine) signalNewWork() {
	// Signal task generation to restart (if stopped!)
	select {
	case e.workSignal <- struct{}{}:
	default:
	}
}
