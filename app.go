package tlock

import (
	"bytes"
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

	"github.com/siddontang/goredis"
)

var errLockTimeout = errors.New("lock timeout")

type App struct {
	m sync.Mutex

	wg sync.WaitGroup

	httpListener net.Listener
	respListener net.Listener

	keyLockerGroup  *KeyLockerGroup
	pathLockerGroup *PathLockerGroup

	locksMutex sync.Mutex
	locks      map[uint64]*lockInfo

	lockIDCounter uint32
}

type lockInfo struct {
	id         uint64
	names      []string
	tp         string
	createTime time.Time
}

func newLockInfo(id uint64, tp string, names []string) *lockInfo {
	l := new(lockInfo)

	l.id = id
	l.names = names
	l.tp = tp
	l.createTime = time.Now()

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
	a.pathLockerGroup = NewPathLockerGroup()

	a.locks = make(map[uint64]*lockInfo, 1024)

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

func (a *App) StartRESP(addr string) error {
	a.m.Lock()
	defer a.m.Unlock()

	var err error
	a.respListener, err = net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()

		for {
			conn, err := a.respListener.Accept()
			if err != nil {
				return
			}

			go a.handleRESP(conn)
		}

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

func (a *App) RESPAddr() net.Addr {
	if a.respListener == nil {
		return nil
	} else {
		return a.respListener.Addr()
	}
}

func (a *App) genLockID() uint64 {
	//todo, optimize later
	id := uint64(time.Now().Unix())
	c := uint64(atomic.AddUint32(&a.lockIDCounter, 1))
	return id<<32 | c
}

// Lock and returns a lock id, you must use this id to unlock
func (a *App) Lock(tp string, names []string) (uint64, error) {
	id, err := a.LockTimeout(tp, InfiniteTimeout, names)
	return id, err
}

// Lock with timeout and returns a lock id, you must use this id to unlock
func (a *App) LockTimeout(tp string, timeout time.Duration, names []string) (uint64, error) {
	if len(names) == 0 {
		return 0, fmt.Errorf("empty lock names")
	}

	var b bool
	var err error
	tp = strings.ToLower(tp)
	c1 := make(chan bool, 1)
	c2 := make(chan bool, 1)
	switch tp {
	case KeyLockType:

		go a.keyLockerGroup.LockTimeout(c1, c2, timeout, names...)

	case PathLockType:
		b, err = a.pathLockerGroup.LockTimeout(timeout, names...), nil
	default:
		return 0, fmt.Errorf("invalid lock type %s", tp)
	}

	select {
	case <-c1:
		id := a.genLockID()
		l := newLockInfo(id, tp, names)

		a.locksMutex.Lock()
		a.locks[id] = l
		a.locksMutex.Unlock()
		return id, nil
	case <-c2:
		return 0, errLockTimeout
	}

	if !b {
		return 0, errLockTimeout
	} else if err != nil {
		return 0, err
	}

	id := a.genLockID()
	l := newLockInfo(id, tp, names)

	a.locksMutex.Lock()
	a.locks[id] = l
	a.locksMutex.Unlock()
	return id, nil
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
		a.keyLockerGroup.Unlock(l.names...)
	case PathLockType:
		a.pathLockerGroup.Unlock(l.names...)
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

// lock name1, name2, ... [TYPE key] [TIMEOUT 60]
// unlock id
func (a *App) handleRESP(c net.Conn) {
	conn, err := goredis.NewConn(c)
	if err != nil {
		c.Close()
		return
	}

	grapLockIDs := make(map[uint64]struct{})

	defer func() {
		conn.Close()
		for id, _ := range grapLockIDs {
			a.Unlock(id)
		}
	}()

	for {
		args, err := conn.ReceiveRequest()
		if err != nil {
			return
		}
		if len(args) < 1 {
			conn.SendValue(fmt.Errorf("empty command"))
			continue
		}

		cmd := strings.ToUpper(string(args[0]))
		args = args[1:]
		switch cmd {
		case "LOCK":
			tp, names, timeout, err := a.parseRESPLock(args)
			if err != nil {
				conn.SendValue(err)
			} else {
				id, err := a.LockTimeout(tp, timeout, names)
				if err != nil {
					conn.SendValue(err)
				} else {
					grapLockIDs[id] = struct{}{}
					conn.SendValue([]byte(strconv.FormatUint(id, 10)))
				}
			}
		case "UNLOCK":
			id, err := a.parseRESPUnlock(args)
			if err != nil {
				conn.SendValue(err)
			} else {
				err = a.Unlock(id)
				if err != nil {
					conn.SendValue(err)
				} else {
					delete(grapLockIDs, id)
					conn.SendValue("OK")
				}
			}
		default:
			conn.SendValue(fmt.Errorf("invalid command %s", cmd))
		}
	}
}

func (a *App) parseRESPLock(args [][]byte) (tp string, names []string, timeout time.Duration, err error) {
	tp = KeyLockType
	timeout = 60 * time.Second

	names = make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := string(args[i])
		s := strings.ToUpper(arg)
		if s == "TYPE" && i < len(args) {
			tp = strings.ToLower(string(args[i+1]))
			i++
		} else if s == "TIMEOUT" && i < len(args) {
			var t uint64
			t, err = strconv.ParseUint(string(args[i+1]), 10, 64)
			if err != nil {
				return
			}
			if t == 0 {
				t = 60
			}

			timeout = time.Duration(t) * time.Second
			i++
		} else {
			names = append(names, arg)
		}
	}
	return
}

func (a *App) parseRESPUnlock(args [][]byte) (id uint64, err error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("empty unlock id")
	}

	return strconv.ParseUint(string(args[0]), 10, 64)
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
		tp := strings.ToLower(r.FormValue("type"))
		if len(tp) == 0 {
			tp = "key"
		}

		id, err := h.a.LockTimeout(tp, time.Duration(timeout)*time.Second, names)
		if err != nil && err != errLockTimeout {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error()))
		} else if err == errLockTimeout {
			w.WriteHeader(http.StatusRequestTimeout)
			w.Write([]byte("Lock timeout"))
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(strconv.FormatUint(id, 10)))
		}
	case "DELETE":
		id, err := strconv.ParseUint(r.FormValue("id"), 10, 64)
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
}
