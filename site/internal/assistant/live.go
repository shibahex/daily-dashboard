package assistant

import "sync"

// LiveAnswer is the question being answered and the reply so far, so a page (or a reload) can
// follow an answer that runs in the background.
type LiveAnswer struct {
	mu       sync.Mutex
	question Message
	reply    Message
	running  bool
	changed  chan struct{} // closed (and replaced) whenever the reply changes
}

func NewLiveAnswer() *LiveAnswer { return &LiveAnswer{changed: make(chan struct{})} }

// Start begins a new answer, unless one is still running.
func (a *LiveAnswer) Start(question string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return false
	}
	a.running = true
	a.question = Message{Role: "user", Text: question}
	a.reply = Message{Role: "assistant"}
	return true
}

func (a *LiveAnswer) Progress(reply Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reply = reply
	a.notify()
}

func (a *LiveAnswer) Finish() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = false
	a.notify()
}

func (a *LiveAnswer) notify() {
	close(a.changed)
	a.changed = make(chan struct{})
}

// Current is the answer as it is now, and a channel that closes when it changes.
func (a *LiveAnswer) Current() (question, reply Message, running bool, changed <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.question, a.reply, a.running, a.changed
}
