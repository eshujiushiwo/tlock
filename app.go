package tlock

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var errLockTimeout = errors.New("lock timeout")

type App struct {
	m sync.Mutex

	wg sync.WaitGroup

	httpListener net.Listener
	respListener net.Listener

	keyLockerGroup *KeyLockerGroup

	locksMutex sync.Mutex
	locks      map[uint64]*lockInfo

	lockIDCounter uint32
	source        chan string
}

type lockInfo struct {
	id         uint64
	names      []string
	tp         string
	createTime time.Time
	mylist     *list.List
}

func newLockInfo(id uint64, tp string, names []string) *lockInfo {
	l := new(lockInfo)

	l.id = id
	l.names = names
	l.tp = tp
	l.createTime = time.Now()
	l.mylist = list.New()

	return l
}

type lockInfos []*lockInfo

func (s lockInfos) Len() int {
	return len(s)
}

func (s lockInfos) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s lockInfos) Less(i, j int) bool {
	return s[i].id < s[j].id
}

func NewApp() *App {
	a := new(App)

	a.keyLockerGroup = NewKeyLockerGroup()

	a.locks = make(map[uint64]*lockInfo, 1024)
	a.source = make(chan string, 1)

	return a
}

func (a *App) StartHTTP(addr string) error {
	a.m.Lock()
	defer a.m.Unlock()

	var err error
	a.httpListener, err = net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()

		mux := http.NewServeMux()
		mux.Handle("/lock", a.newLockHandler())

		http.Serve(a.httpListener, mux)

	}()
	return nil
}

func (a *App) Close() {
	a.m.Lock()
	defer a.m.Unlock()

	if a.httpListener != nil {
		a.httpListener.Close()
	}

	if a.respListener != nil {
		a.respListener.Close()
	}

	a.wg.Wait()
}

func (a *App) HTTPAddr() net.Addr {
	if a.httpListener == nil {
		return nil
	} else {
		return a.httpListener.Addr()
	}
}

func (a *App) genLockID() uint64 {
	//todo, optimize later
	id := uint64(time.Now().Unix())
	c := uint64(atomic.AddUint32(&a.lockIDCounter, 1))
	return id<<32 | c
}

// Lock and returns a lock id, you must use this id to unlock
// func (a *App) Lock(tp string, names []string) (uint64, error) {
// 	id, err := a.LockTimeout(tp, InfiniteTimeout, names)
// 	return id, err
// }

// Lock with timeout and returns a lock id, you must use this id to unlock
func (a *App) LockTimeout(ctx context.Context, chmsg chan string, chn chan uint64, errs chan error, tp string, timeout time.Duration, names []string) {

	if len(names) == 0 {
		errs <- fmt.Errorf("empty lock names")

	}

	ctxl, _ := context.WithTimeout(ctx, timeout)

	tp = strings.ToLower(tp)
	c1 := make(chan bool, 1)
	c2 := make(chan bool, 1)
	switch tp {
	case KeyLockType:
		a.keyLockerGroup.LockTimeout(ctxl, c1, c2, timeout, names...)

	default:
		fmt.Errorf("invalid lock type %s", tp)
	}

	select {

	case <-c1:

		id := a.genLockID()
		l := newLockInfo(id, tp, names)

		a.locksMutex.Lock()
		a.locks[id] = l
		a.locksMutex.Unlock()

		chn <- id

		select {
		case <-ctx.Done():
			fmt.Println(id)

			fmt.Println("show up")
			//a.source <- msg11

			for _, key := range names {
				for range a.keyLockerGroup.getSet(key).workers {
					l.mylist.PushBack(1)

				}
			}

			a.Unlock(id)

		}

	case <-c2:
		errs <- errLockTimeout
	case msg := <-a.source:

		fmt.Println("999999999999999")
		chmsg <- msg
		return
	}

}

func (a *App) Unlock(id uint64) error {
	if id == 0 {
		return fmt.Errorf("empty lock names")
	}

	a.locksMutex.Lock()
	l, ok := a.locks[id]
	delete(a.locks, id)
	a.locksMutex.Unlock()

	if !ok {
		return nil
	}

	switch l.tp {
	case KeyLockType:
		fmt.Println(l.names)
		a.keyLockerGroup.Unlock(l.names...)

	default:
		return fmt.Errorf("invalid lock type %s", l.tp)
	}

	return nil
}

const timeFormat string = "2006-01-02 15:04:05"

func (a *App) dumpLockNames() []byte {
	var buf bytes.Buffer

	keyLocks := make(lockInfos, 0, 1024)
	pathLocks := make(lockInfos, 0, 1024)

	a.locksMutex.Lock()
	for _, l := range a.locks {
		if l.tp == KeyLockType {
			keyLocks = append(keyLocks, l)
		} else {
			pathLocks = append(pathLocks, l)
		}
	}
	a.locksMutex.Unlock()

	sort.Sort(keyLocks)
	sort.Sort(pathLocks)

	buf.WriteString("key lock:\n")
	for _, l := range keyLocks {
		buf.WriteString(fmt.Sprintf("%d %v\t%s\n", l.id, l.names, l.createTime.Format(timeFormat)))
	}

	buf.WriteString("\npath lock:\n")
	for _, l := range pathLocks {
		buf.WriteString(fmt.Sprintf("%d %v\t%s\n", l.id, l.names, l.createTime.Format(timeFormat)))
	}

	return buf.Bytes()
}

type lockHandler struct {
	a *App
}

func (a *App) newLockHandler() *lockHandler {
	h := new(lockHandler)
	h.a = a

	return h
}

// Lock:   Post/Put /lock?names=a,b,c&timeout=10&type=key return a lock id
// Unlock: Delete   /lock?id=lockid
// For HTTP, the default and maximum timeout is 60s
// Lock type supports key and path, the default is key
// List locks: Get  /lock
func (h *lockHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var (
		ctx context.Context
		//cancel context.CancelFunc
	)

	errs := make(chan error, 1)
	switch r.Method {
	case "GET":
		buf := h.a.dumpLockNames()
		w.Header().Set("Content-Type", "text/plain")
		w.Write(buf)
		return
	case "POST", "PUT":

		names := strings.Split(r.FormValue("names"), ",")
		if len(names) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("empty lock names"))
			return
		}

		timeout, _ := strconv.Atoi(r.FormValue("timeout"))

		if timeout <= 0 {
			timeout = 60
		}
		cccc := time.Duration(timeout) * time.Second
		ctx, _ = context.WithTimeout(context.Background(), cccc)

		tp := strings.ToLower(r.FormValue("type"))

		if len(tp) == 0 {
			tp = "key"
		}
		chn := make(chan uint64, 1)
		chmsg := make(chan string, 1)
		go func() {
			h.a.LockTimeout(ctx, chmsg, chn, errs, tp, time.Duration(timeout)*time.Second, names)
		}()

		select {
		case <-chmsg:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("aaaaa"))
			return
		case id := <-chn:

			w.WriteHeader(http.StatusOK)
			w.Write([]byte(strconv.FormatUint(id, 10)))
		case err := <-errs:

			if err != nil && err != errLockTimeout {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(err.Error()))
			} else if err == errLockTimeout {
				w.WriteHeader(http.StatusRequestTimeout)
				w.Write([]byte("Lock timeout"))
			}
		}

	case "DELETE":
		id, err := strconv.ParseUint(r.FormValue("id"), 10, 64)
		id1 := r.FormValue("id")
		fmt.Println(id1)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error()))
			return
		}

		err = h.a.Unlock(id)

		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error()))
		} else {
			w.WriteHeader(http.StatusOK)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	//defer cancel()
}
