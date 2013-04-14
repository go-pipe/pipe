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
type Pipe func(*State) error

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

	tasks []*task
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

type task struct {
	s    State
	f    func(*State) error
	done func() error
	kill func() error
}

func dummy() error { return nil }

// Task creates a new task that can block reading from and/or
// writing to the state streams.
// Tasks are all run concurrently, and must only be run if
// all the involved Pipe functions have returned no errors.
func (s *State) Task(f func(*State) error) {
	t := &task{*s, f, dummy, dummy}
	t.s.Env = append([]string(nil), s.Env...)
	s.tasks = append(s.tasks, t)
}

// TaskKill registers a function that must be called if interrupting
// the previous task registered in s is necessary. Functions that are
// blocked simply reading from and/or writing to the state streams
// do not need a kill function, as they will be unblocked by the
// closing of the streams used.
func (s *State) TaskKill(f func() error) {
	task := s.tasks[len(s.tasks)-1]
	task.kill = chain(task.kill, f)
}

// TaskDone registers a function to be executed after the
// previous task created in s finishes running.
func (s *State) TaskDone(f func() error) {
	task := s.tasks[len(s.tasks)-1]
	task.done = chain(task.done, f)
}

func chain(f1, f2 func() error) func() error {
	return func() error { return firstErr(f1(), f2()) }
}

func firstErr(err1, err2 error) error {
	if err1 != nil {
		return err1
	}
	return err2
}

// RunTasks runs all tasks that were registered in the pipe state.
func (s *State) RunTasks() error {
	done := make(chan error, len(s.tasks))
	for i := range s.tasks {
		task := s.tasks[i]
		go func() {
			done <- firstErr(task.f(&task.s), task.done())
		}()
	}
	var first error
	for _ = range s.tasks {
		err := <-done
		if err != nil && first == nil {
			first = err
			for i := range s.tasks {
				s.tasks[i].kill()
			}
		}
	}
	s.tasks = nil
	return first
}

func Run(p Pipe) error {
	s := NewState(nil, nil)
	err := p(s)
	if err == nil {
		err = s.RunTasks()
	}
	return err
}

func Output(p Pipe) ([]byte, error) {
	outb := &OutputBuffer{}
	s := NewState(outb, nil)
	err := p(s)
	if err == nil {
		err = s.RunTasks()
	}
	return outb.Bytes(), err
}

func CombinedOutput(p Pipe) ([]byte, error) {
	outb := &OutputBuffer{}
	s := NewState(outb, outb)
	err := p(s)
	if err == nil {
		err = s.RunTasks()
	}
	return outb.Bytes(), err
}

func DisjointOutput(p Pipe) (stdout []byte, stderr []byte, err error) {
	outb := &OutputBuffer{}
	errb := &OutputBuffer{}
	s := NewState(outb, errb)
	err = p(s)
	if err == nil {
		err = s.RunTasks()
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
		ch := make(chan *os.Process, 1)
		s.Task(func(s *State) error {
			cmd := exec.Command(name, args...)
			cmd.Dir = s.Dir
			cmd.Env = s.Env
			cmd.Stdin = s.Stdin
			cmd.Stdout = s.Stdout
			cmd.Stderr = s.Stderr
			err := cmd.Start()
			ch <- cmd.Process
			if err != nil {
				return err
			}
			if err := cmd.Wait(); err != nil {
				return fmt.Errorf("command %q: %v", name, err)
			}
			return nil
		})
		s.TaskKill(func() error {
			if p, ok := <-ch; ok {
				p.Kill()
			}
			return nil
		})
		return nil
	}
}

func System(command string) Pipe {
	return Exec("/bin/sh", "-c", command)
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
			closer := r
			if i == end {
				r, w = nil, nil
				s.Stdout = endStdout
			} else {
				r, w = io.Pipe()
				s.Stdout = w
			}
			closew := w
			closef := func() error {
				if closew != nil {
					closew.Close()
				}
				if closer != nil {
					//io.Copy(ioutil.Discard, closer)
					closer.Close()
				}
				return nil
			}
			if err := p(s); err != nil {
				closef()
				return err
			}
			s.TaskDone(closef)
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
			if err := s.RunTasks(); err != nil {
				return err
			}
		}
		s.Env = env
		return nil
	}
}

func Echo(str string) Pipe {
	return func(s *State) error {
		s.Task(func(s *State) error {
			_, err := s.Stdout.Write([]byte(str))
			return err
		})
		return nil
	}
}

func Read(r io.Reader) Pipe {
	return func(s *State) error {
		s.Task(func(s *State) error {
			_, err := io.Copy(s.Stdout, r)
			return err
		})
		return nil
	}
}

func Write(w io.Writer) Pipe {
	return func(s *State) error {
		s.Task(func(s *State) error {
			_, err := io.Copy(w, s.Stdin)
			return err
		})
		return nil
	}
}

func Discard() Pipe {
	return Write(ioutil.Discard)
}

func Tee(w io.Writer) Pipe {
	return func(s *State) error {
		s.Task(func(s *State) error {
			_, err := io.Copy(w, io.TeeReader(s.Stdin, s.Stdout))
			return err
		})
		return nil
	}
}

func ReadFile(path string) Pipe {
	return func(s *State) error {
		s.Task(func(s *State) error {
			file, err := os.Open(filepath.Join(s.Dir, path))
			if err != nil {
				return err
			}
			_, err = io.Copy(s.Stdout, file)
			file.Close()
			return err
		})
		return nil
	}
}

func WriteFile(path string, perm os.FileMode) Pipe {
	return func(s *State) error {
		s.Task(func(s *State) error {
			path = filepath.Join(s.Dir, path)
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
			if err != nil {
				return err
			}
			_, err = io.Copy(file, s.Stdin)
			return firstErr(err, file.Close())
		})
		return nil
	}
}

func TeeFile(path string, perm os.FileMode) Pipe {
	return func(s *State) error {
		s.Task(func(s *State) error {
			// BUG: filepath.Join missing; test it first.
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
			if err != nil {
				return err
			}
			_, err = io.Copy(file, io.TeeReader(s.Stdin, s.Stdout))
			return firstErr(err, file.Close())
		})
		return nil
	}
}

//pipe.If(pipe.IsFile("/foo"), func(p pipe.Line) (Line, error) {
