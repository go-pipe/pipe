package pipe

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Pipe functions implement arbitrary functionality that may be
// plugged within pipe scripts and pipe lines. Pipe functions must
// not block reading or writing to the state streams. These
// operations must be run within state tasks (see State.Task).

// State defines the environment for Pipe functions to run on.
// Create a new State via the NewState function.
type State struct {

	// Stdin, Stdout, and Stderr represent the respective data
	// streams that the Pipe may choose to manipulate when running.
	// Reading from and writing to these streams must be done from
	// within a task created via the Task method.
	// The three streams are initialized by NewState and must
	// never be set to nil.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Dir represents the directory in which all filesystem-related
	// operations performed by the Pipe must be run on. It defaults
	// to the current directory, and may be changed by Pipe functions,
	// although changes must not be done from tasks or other goroutines.
	Dir string

	// Env is the process environment in which all executions performed
	// by the Pipe must be run on. It defaults to a copy of the
	// environmnet from the current process, and may be changed by Pipe
	// functions, although changes must not be done from tasks or other
	// goroutines.
	Env []string

	pendingFlushes []*pendingFlush
}

type pendingFlush struct {
	s State
	f Flusher
	c []io.Closer
}

func (pf *pendingFlush) closeWhenDone(c io.Closer) {
	pf.c = append(pf.c, c)
}

func (pf *pendingFlush) closeAll() {
	for _, c := range pf.c {
		c.Close()
	}
}

type Pipe func(s *State) error

type Flusher interface {
	Flush(s *State) error
	Kill()
}

func (s *State) AddFlusher(f Flusher) error {
	pf := &pendingFlush{*s, f, nil}
	pf.s.Env = append([]string(nil), s.Env...)
	s.pendingFlushes = append(s.pendingFlushes, pf)
	return nil
}

func (s *State) FlushAll() error {
	done := make(chan error, len(s.pendingFlushes))
	for _, f := range s.pendingFlushes {
		go func(pf *pendingFlush) {
			err := pf.f.Flush(&pf.s)
			pf.closeAll()
			done <- err
		}(f)
	}
	var first error
	for _ = range s.pendingFlushes {
		err := <-done
		if err != nil && first == nil {
			first = err
			for _, pf := range s.pendingFlushes {
				pf.f.Kill()
			}
		}
	}
	s.pendingFlushes = nil
	return first
}

type flusherFunc func(s *State) error

func (f flusherFunc) Flush(s *State) error { return f(s) }
func (f flusherFunc) Kill()                {}

func FlusherFunc(f func(s *State) error) Pipe {
	return func(s *State) error {
		s.AddFlusher(flusherFunc(f))
		return nil
	}
}

// NewState returns a new state for running pipes with.
func NewState(stdout, stderr io.Writer) *State {
	if stdout == nil {
		stdout = ioutil.Discard
	}
	if stderr == nil {
		stderr = ioutil.Discard
	}
	return &State{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
		Env:    os.Environ(),
	}
}

// EnvVar returns the value for the named environment variable in s.
func (s *State) EnvVar(name string) string {
	prefix := name + "="
	for _, kv := range s.Env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

// SetEnvVar sets the named environment variable to the given value in s.
func (s *State) SetEnvVar(name, value string) {
	prefix := name + "="
	for i, kv := range s.Env {
		if strings.HasPrefix(kv, prefix) {
			s.Env[i] = prefix + value
			return
		}
	}
	s.Env = append(s.Env, prefix+value)
}

func firstErr(err1, err2 error) error {
	if err1 != nil {
		return err1
	}
	return err2
}

func Run(p Pipe) error {
	s := NewState(nil, nil)
	err := p(s)
	if err == nil {
		err = s.FlushAll()
	}
	return err
}

func Output(p Pipe) ([]byte, error) {
	outb := &OutputBuffer{}
	s := NewState(outb, nil)
	err := p(s)
	if err == nil {
		err = s.FlushAll()
	}
	return outb.Bytes(), err
}

func CombinedOutput(p Pipe) ([]byte, error) {
	outb := &OutputBuffer{}
	s := NewState(outb, outb)
	err := p(s)
	if err == nil {
		err = s.FlushAll()
	}
	return outb.Bytes(), err
}

func DisjointOutput(p Pipe) (stdout []byte, stderr []byte, err error) {
	outb := &OutputBuffer{}
	errb := &OutputBuffer{}
	s := NewState(outb, errb)
	err = p(s)
	if err == nil {
		err = s.FlushAll()
	}
	return outb.Bytes(), errb.Bytes(), err
}

type OutputBuffer struct {
	m   sync.Mutex
	buf []byte
}

func (out *OutputBuffer) Write(p []byte) (n int, err error) {
	out.m.Lock()
	out.buf = append(out.buf, p...)
	out.m.Unlock()
	return len(p), nil
}

func (out *OutputBuffer) Bytes() []byte {
	out.m.Lock()
	buf := out.buf
	out.m.Unlock()
	return buf
}

func Exec(name string, args ...string) Pipe {
	return func(s *State) error {
		s.AddFlusher(&execFlusher{name, args, make(chan *os.Process, 1)})
		return nil
	}
}

func System(command string) Pipe {
	return Exec("/bin/sh", "-c", command)
}

type execFlusher struct {
	name string
	args []string
	ch   chan *os.Process
}

func (f *execFlusher) Flush(s *State) error {
	cmd := exec.Command(f.name, f.args...)
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	cmd.Stdin = s.Stdin
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	err := cmd.Start()
	f.ch <- cmd.Process
	if err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("command %q: %v", f.name, err)
	}
	return nil
}

func (f *execFlusher) Kill() {
	if p, ok := <-f.ch; ok {
		p.Kill()
	}
}

func ChDir(dir string) Pipe {
	return func(s *State) error {
		s.Dir = filepath.Join(s.Dir, dir)
		return nil
	}
}

func MkDir(dir string, perm os.FileMode) Pipe {
	return func(s *State) error {
		return os.Mkdir(filepath.Join(s.Dir, dir), perm)
	}
}

func SetEnvVar(name string, value string) Pipe {
	return func(s *State) error {
		s.SetEnvVar(name, value)
		return nil
	}
}

func CombineToErr() Pipe {
	return func(s *State) error {
		s.Stdout = s.Stderr
		return nil
	}
}

func CombineToOut() Pipe {
	return func(s *State) error {
		s.Stderr = s.Stdout
		return nil
	}
}

func Line(p ...Pipe) Pipe {
	return func(s *State) error {
		end := len(p) - 1
		endStdout := s.Stdout
		var r *io.PipeReader
		var w *io.PipeWriter
		for i, p := range p {
			closeIn := r
			if i == end {
				r, w = nil, nil
				s.Stdout = endStdout
			} else {
				r, w = io.Pipe()
				s.Stdout = w
			}
			closeOut := w

			oldLen := len(s.pendingFlushes)
			if err := p(s); err != nil {
				if closeIn != nil {
					closeIn.Close()
				}
				return err
			}
			newLen := len(s.pendingFlushes)

			// Close the created ends that were put in place for this
			// specific Pipe after the last flusher that was registered
			// as a consequence of running the given Pipe ends running.
			if newLen == oldLen {
				if closeIn != nil {
					closeIn.Close()
				}
				if closeOut != nil {
					closeOut.Close()
				}
			} else {
				if closeIn != nil {
					for fi := oldLen; fi < newLen+1; fi++ {
						if fi == newLen || s.pendingFlushes[fi].s.Stdin != closeIn {
							s.pendingFlushes[fi-1].closeWhenDone(closeIn)
						}
					}
				}
				if closeOut != nil {
					for fi := newLen - 1; fi >= oldLen; fi-- {
						if fi == oldLen || (s.pendingFlushes[fi].s.Stdout == closeOut || s.pendingFlushes[fi].s.Stderr == closeOut) {
							s.pendingFlushes[fi].closeWhenDone(closeOut)
						}
					}
				}
			}

			if i < end {
				s.Stdin = r
			}
		}
		return nil
	}
}

func Script(p ...Pipe) Pipe {
	return func(s *State) error {
		env := append([]string(nil), s.Env...)
		for _, p := range p {
			if err := p(s); err != nil {
				return err
			}
			if err := s.FlushAll(); err != nil {
				return err
			}
		}
		s.Env = env
		return nil
	}
}

func Echo(str string) Pipe {
	return FlusherFunc(func(s *State) error {
		_, err := s.Stdout.Write([]byte(str))
		return err
	})
}

func Read(r io.Reader) Pipe {
	return FlusherFunc(func(s *State) error {
		_, err := io.Copy(s.Stdout, r)
		return err
	})
}

func Write(w io.Writer) Pipe {
	return FlusherFunc(func(s *State) error {
		_, err := io.Copy(w, s.Stdin)
		return err
	})
}

func Discard() Pipe {
	return Write(ioutil.Discard)
}

func Tee(w io.Writer) Pipe {
	return FlusherFunc(func(s *State) error {
		_, err := io.Copy(w, io.TeeReader(s.Stdin, s.Stdout))
		return err
	})
}

func ReadFile(path string) Pipe {
	return FlusherFunc(func(s *State) error {
		file, err := os.Open(filepath.Join(s.Dir, path))
		if err != nil {
			return err
		}
		_, err = io.Copy(s.Stdout, file)
		file.Close()
		return err
	})
}

func WriteFile(path string, perm os.FileMode) Pipe {
	return FlusherFunc(func(s *State) error {
		path = filepath.Join(s.Dir, path)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
		if err != nil {
			return err
		}
		_, err = io.Copy(file, s.Stdin)
		return firstErr(err, file.Close())
	})
}

func TeeFile(path string, perm os.FileMode) Pipe {
	return FlusherFunc(func(s *State) error {
		// BUG: filepath.Join missing; test it first.
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
		if err != nil {
			return err
		}
		_, err = io.Copy(file, io.TeeReader(s.Stdin, s.Stdout))
		return firstErr(err, file.Close())
	})
}

//pipe.If(pipe.IsFile("/foo"), func(p pipe.Line) (Line, error) {
