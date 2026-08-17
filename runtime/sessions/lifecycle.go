package sessions

import "sync"

// drainClose serializes Close against in-flight operations. FileStore and
// Manager share this admission/drain protocol.
type drainClose struct {
	mu        sync.Mutex
	cond      *sync.Cond
	active    int
	closing   bool
	closeDone chan struct{}
	closeErr  error
}

func (d *drainClose) init() {
	d.closeDone = make(chan struct{})
	d.cond = sync.NewCond(&d.mu)
}

func (d *drainClose) enter() error {
	d.mu.Lock()
	if d.closing {
		d.mu.Unlock()
		return ErrClosed
	}
	d.active++
	d.mu.Unlock()
	return nil
}

func (d *drainClose) leave() {
	d.mu.Lock()
	d.active--
	if d.active == 0 {
		d.cond.Broadcast()
	}
	d.mu.Unlock()
}

func (d *drainClose) close(fn func() error) error {
	d.mu.Lock()
	if d.closing {
		done := d.closeDone
		d.mu.Unlock()
		<-done
		d.mu.Lock()
		err := d.closeErr
		d.mu.Unlock()
		return err
	}
	d.closing = true
	for d.active != 0 {
		d.cond.Wait()
	}
	d.mu.Unlock()

	err := fn()
	d.mu.Lock()
	d.closeErr = err
	close(d.closeDone)
	d.mu.Unlock()
	return err
}
