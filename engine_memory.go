package centrifuge

import (
	"container/heap"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge/internal/priority"
)

// MemoryEngine allows to run Centrifugo without using Redis at all.
// All data managed inside process memory. With this engine you can
// only run single Centrifugo node. If you need to scale you should
// use Redis engine instead.
type MemoryEngine struct {
	node         *Node
	eventHandler EngineEventHandler
	presenceHub  *presenceHub
	historyHub   *historyHub
	// db           *badger.DB
}

// MemoryEngineConfig is a memory engine config.
type MemoryEngineConfig struct{}

// NewMemoryEngine initializes Memory Engine.
func NewMemoryEngine(n *Node, conf MemoryEngineConfig) (*MemoryEngine, error) {
	// opts := badger.DefaultOptions
	// opts.Dir = "/tmp/centrifuge"
	// opts.ValueDir = "/tmp/centrifuge"
	// db, err := badger.Open(opts)
	// if err != nil {
	// 	log.Fatal(err)
	// }
	e := &MemoryEngine{
		node:        n,
		presenceHub: newPresenceHub(),
		historyHub:  newHistoryHub(),
		// db:          db,
	}
	e.historyHub.initialize()
	return e, nil
}

// Run runs memory engine - we do not have any logic here as Memory Engine ready to work
// just after initialization.
func (e *MemoryEngine) run(h EngineEventHandler) error {
	e.eventHandler = h
	return nil
}

// func (e *MemoryEngine) shutdown(h EngineEventHandler) error {
// 	return e.db.Close()
// }

// Publish adds message into history hub and calls node ClientMsg method to handle message.
// We don't have any PUB/SUB here as Memory Engine is single node only.
func (e *MemoryEngine) publish(ch string, pub *Publication, opts *ChannelOptions) <-chan error {

	hasCurrentSubscribers := e.node.hub.NumSubscribers(ch) > 0

	if opts != nil && opts.HistorySize > 0 && opts.HistoryLifetime > 0 {
		err := e.historyHub.add(ch, pub, opts, hasCurrentSubscribers)
		if err != nil {
			e.node.logger.log(newLogEntry(LogLevelError, "error adding into history hub", map[string]interface{}{"error": err.Error()}))
		}
	}

	eChan := make(chan error, 1)
	eChan <- e.eventHandler.HandlePublication(ch, pub)
	return eChan
}

// PublishJoin - see engine interface description.
func (e *MemoryEngine) publishJoin(ch string, join *Join, opts *ChannelOptions) <-chan error {
	eChan := make(chan error, 1)
	eChan <- e.eventHandler.HandleJoin(ch, join)
	return eChan
}

// PublishLeave - see engine interface description.
func (e *MemoryEngine) publishLeave(ch string, leave *Leave, opts *ChannelOptions) <-chan error {
	eChan := make(chan error, 1)
	eChan <- e.eventHandler.HandleLeave(ch, leave)
	return eChan
}

// PublishControl - see Engine interface description.
func (e *MemoryEngine) publishControl(data []byte) <-chan error {
	eChan := make(chan error, 1)
	eChan <- e.eventHandler.HandleControl(data)
	return eChan
}

// Subscribe is noop here.
func (e *MemoryEngine) subscribe(ch string) error {
	return nil
}

// Unsubscribe node from Channel In case of memory engine its only job is
// to touch channel history for history lifetime period.
// See https://github.com/centrifugal/centrifugo/pull/148
func (e *MemoryEngine) unsubscribe(ch string) error {
	if chOpts, ok := e.node.ChannelOpts(ch); ok && chOpts.HistoryDropInactive {
		e.historyHub.touch(ch, &chOpts)
	}
	return nil
}

// AddPresence - see engine interface description.
func (e *MemoryEngine) addPresence(ch string, uid string, info *ClientInfo, exp time.Duration) error {
	return e.presenceHub.add(ch, uid, info)
}

// RemovePresence - see engine interface description.
func (e *MemoryEngine) removePresence(ch string, uid string) error {
	return e.presenceHub.remove(ch, uid)
}

// Presence - see engine interface description.
func (e *MemoryEngine) presence(ch string) (map[string]*ClientInfo, error) {
	return e.presenceHub.get(ch)
}

// PresenceStats - see engine interface description.
func (e *MemoryEngine) presenceStats(ch string) (PresenceStats, error) {
	return e.presenceHub.getStats(ch)
}

// History - see engine interface description.
func (e *MemoryEngine) history(ch string, limit int) ([]*Publication, error) {
	return e.historyHub.get(ch, limit)
}

// History - see engine interface description.
func (e *MemoryEngine) historyLastID(ch string) (uint64, error) {
	return e.historyHub.getLastID(ch), nil
}

// RecoverHistory - see engine interface description.
func (e *MemoryEngine) recoverHistory(ch string, fromID uint64) ([]*Publication, bool, error) {
	return e.historyHub.recover(ch, fromID)
}

// RemoveHistory - see engine interface description.
func (e *MemoryEngine) removeHistory(ch string) error {
	return e.historyHub.remove(ch)
}

// Channels - see engine interface description.
func (e *MemoryEngine) channels() ([]string, error) {
	return e.node.hub.Channels(), nil
}

type presenceHub struct {
	sync.RWMutex
	presence map[string]map[string]*ClientInfo
}

func newPresenceHub() *presenceHub {
	return &presenceHub{
		presence: make(map[string]map[string]*ClientInfo),
	}
}

func (h *presenceHub) add(ch string, uid string, info *ClientInfo) error {
	h.Lock()
	defer h.Unlock()

	_, ok := h.presence[ch]
	if !ok {
		h.presence[ch] = make(map[string]*ClientInfo)
	}
	h.presence[ch][uid] = info
	return nil
}

func (h *presenceHub) remove(ch string, uid string) error {
	h.Lock()
	defer h.Unlock()

	if _, ok := h.presence[ch]; !ok {
		return nil
	}
	if _, ok := h.presence[ch][uid]; !ok {
		return nil
	}

	delete(h.presence[ch], uid)

	// clean up map if needed
	if len(h.presence[ch]) == 0 {
		delete(h.presence, ch)
	}

	return nil
}

func (h *presenceHub) get(ch string) (map[string]*ClientInfo, error) {
	h.RLock()
	defer h.RUnlock()

	presence, ok := h.presence[ch]
	if !ok {
		// return empty map
		return nil, nil
	}

	data := make(map[string]*ClientInfo, len(presence))
	for k, v := range presence {
		data[k] = v
	}
	return data, nil
}

func (h *presenceHub) getStats(ch string) (PresenceStats, error) {
	h.RLock()
	defer h.RUnlock()

	presence, ok := h.presence[ch]
	if !ok {
		// return empty map
		return PresenceStats{}, nil
	}

	numClients := len(presence)
	numUsers := 0
	uniqueUsers := map[string]struct{}{}

	for _, info := range presence {
		userID := info.User
		if _, ok := uniqueUsers[userID]; !ok {
			uniqueUsers[userID] = struct{}{}
			numUsers++
		}
	}

	return PresenceStats{
		NumClients: numClients,
		NumUsers:   numUsers,
	}, nil
}

type historyItem struct {
	messages []*Publication
	expireAt int64
}

func (i historyItem) isExpired() bool {
	return i.expireAt < time.Now().Unix()
}

type channelTop struct {
	ID  uint64
	UID string
}

type historyHub struct {
	sync.RWMutex
	history   map[string]historyItem
	queue     priority.Queue
	nextCheck int64

	// db *badger.DB

	topsMu sync.RWMutex
	tops   map[string]uint64
}

func newHistoryHub() *historyHub {
	return &historyHub{
		history:   make(map[string]historyItem),
		queue:     priority.MakeQueue(),
		nextCheck: 0,
		// db:        db,
		tops: make(map[string]uint64),
	}
}

func (h *historyHub) initialize() {
	go h.expire()
}

func (h *historyHub) expire() {
	var nextCheck int64
	for {
		time.Sleep(time.Second)
		h.Lock()
		if h.nextCheck == 0 || h.nextCheck > time.Now().Unix() {
			h.Unlock()
			continue
		}
		nextCheck = 0
		for h.queue.Len() > 0 {
			item := heap.Pop(&h.queue).(*priority.Item)
			expireAt := item.Priority
			if expireAt > time.Now().Unix() {
				heap.Push(&h.queue, item)
				nextCheck = expireAt
				break
			}
			ch := item.Value
			hItem, ok := h.history[ch]
			if !ok {
				continue
			}
			if hItem.expireAt <= expireAt {
				delete(h.history, ch)
			}
		}
		h.nextCheck = nextCheck
		h.Unlock()
	}
}

func (h *historyHub) nextID(ch string) uint64 {
	var val uint64
	h.topsMu.Lock()
	top, ok := h.tops[ch]
	if !ok {
		val = 1
		h.tops[ch] = val
	} else {
		top++
		h.tops[ch] = top
		val = top
	}
	h.topsMu.Unlock()
	return val
}

func (h *historyHub) getLastID(ch string) uint64 {
	h.topsMu.Lock()
	defer h.topsMu.Unlock()
	top, ok := h.tops[ch]
	if !ok {
		var top uint64
		h.tops[ch] = top
		return top
	}
	return top
}

func (h *historyHub) touch(ch string, opts *ChannelOptions) {
	h.Lock()
	defer h.Unlock()

	item, ok := h.history[ch]
	expireAt := time.Now().Unix() + int64(opts.HistoryLifetime)

	heap.Push(&h.queue, &priority.Item{Value: ch, Priority: expireAt})

	if !ok {
		h.history[ch] = historyItem{
			messages: []*Publication{},
			expireAt: expireAt,
		}
	} else {
		item.expireAt = expireAt
	}

	if h.nextCheck == 0 || h.nextCheck > expireAt {
		h.nextCheck = expireAt
	}
}

func (h *historyHub) add(ch string, pub *Publication, opts *ChannelOptions, hasSubscribers bool) error {
	h.Lock()
	defer h.Unlock()

	pub.ID = h.nextID(ch)

	_, ok := h.history[ch]

	if opts.HistoryDropInactive && !hasSubscribers && !ok {
		// No active history for this channel so don't bother storing at all
		return nil
	}

	expireAt := time.Now().Unix() + int64(opts.HistoryLifetime)
	heap.Push(&h.queue, &priority.Item{Value: ch, Priority: expireAt})
	if !ok {
		h.history[ch] = historyItem{
			messages: []*Publication{pub},
			expireAt: expireAt,
		}
	} else {
		messages := h.history[ch].messages
		messages = append([]*Publication{pub}, messages...)
		if len(messages) > opts.HistorySize {
			messages = messages[0:opts.HistorySize]
		}
		h.history[ch] = historyItem{
			messages: messages,
			expireAt: expireAt,
		}
	}

	if h.nextCheck == 0 || h.nextCheck > expireAt {
		h.nextCheck = expireAt
	}

	return nil
}

func (h *historyHub) get(ch string, limit int) ([]*Publication, error) {
	h.RLock()
	defer h.RUnlock()

	hItem, ok := h.history[ch]
	if !ok {
		// return empty slice
		return []*Publication{}, nil
	}
	if hItem.isExpired() {
		// return empty slice
		delete(h.history, ch)
		return []*Publication{}, nil
	}
	if limit == 0 || limit >= len(hItem.messages) {
		return hItem.messages, nil
	}
	return hItem.messages[:limit], nil
}

func (h *historyHub) remove(ch string) error {
	h.RLock()
	defer h.RUnlock()

	_, ok := h.history[ch]
	if ok {
		delete(h.history, ch)
	}
	return nil
}

func (h *historyHub) recover(ch string, fromID uint64) ([]*Publication, bool, error) {
	lastID := h.getLastID(ch)

	if fromID == lastID {
		return nil, true, nil
	}

	if lastID <= fromID {
		return nil, false, nil
	}

	publications, err := h.get(ch, 0)
	if err != nil {
		return nil, false, err
	}

	if len(publications) > 0 {
		if publications[0].ID == fromID {
			return nil, true, nil
		}
		if publications[0].ID < fromID {
			return nil, false, nil
		}
	}

	position := -1

	for i := len(publications) - 1; i >= 0; i-- {
		msg := publications[i]
		if msg.ID == fromID+1 {
			position = i
			break
		}
	}
	if position > -1 {
		// ID found in history. In this case we can be
		// sure that all missed publications recovered.
		return publications[0 : position+1], true, nil
	}

	// Provided ID not found in history. This means that
	// client most probably missed too many messages or publications
	// already expired (or maybe wrong last id provided but
	// it's not a normal case). So we try to compensate as many as we
	// can but get caller know that state is not recovered.
	return publications, false, nil
}
