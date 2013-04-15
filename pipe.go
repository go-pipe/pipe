// pipe - Unix-like pipelines for Go
// 
// Copyright (c) 2010-2012 - Gustavo Niemeyer <gustavo@niemeyer.net>
// 
// All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are met: 
// 
// 1. Redistributions of source code must retain the above copyright notice, this
//    list of conditions and the following disclaimer. 
// 2. Redistributions in binary form must reproduce the above copyright notice,
//    this list of conditions and the following disclaimer in the documentation
//    and/or other materials provided with the distribution. 
// 
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
// ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
// WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
// DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR CONTRIBUTORS BE LIABLE FOR
// ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
// (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES;
// LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND
// ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
// SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

// Package pipe implements unix-like pipelines for Go.
//
// See the documentation for details:
//
//   http://labix.org/pipe
//
package pipe

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// Pipe functions implement arbitrary functionality that may be
// integrated with pipe scripts and pipe lines. Pipe functions
// must not block reading or writing to the state streams. These
// operations must be run from a Flusher.
type Pipe func(s *State) error

// A Flusher is responsible for flowing data from the input
// stream and/or to the output streams of the pipe.
type Flusher interface {

	// Flush flows data from the input stream and/or to the output
	// streams of the pipe. It must block while doing so, and only
	// return once its activities have terminated completely.
	// It is run concurrently with other flushers.
	Flush(s *State) error

	// Kill abruptly interrupts in-progress activities of Flush if errors
	// have happened elsewhere. If Flush is blocked simply reading from
	// and/or writing to the state streams, Kill doesn't have to do
	// anything as Flush will be unblocked by the closing of the streams.
	Kill()
}

// State defines the environment for Pipe functions to run on.
// Create a new State via the NewState function.
type State struct {

	// Stdin, Stdout, and Stderr represent the respective data streams
	// that the Pipe may act upon. Reading from and/or writing to these
	// streams must be done from within a Flusher registered via
	// the AddFlusher method.
	// The three streams are initialized by NewState and must
	// never be set to nil.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Dir represents the directory in which all filesystem-related
	// operations performed by the Pipe must be run on. It defaults
	// to the current directory, and may be changed by Pipe functions.
	Dir string

	// Env is the process environment in which all executions performed
	// by the Pipe must be run on. It defaults to a copy of the
	// environmnet from the current process, and may be changed by Pipe
	// functions.
	Env []string

	pendingFlushes []*pendingFlush
}

// NewState returns a new state for running pipes with.
// The state's Stdout and Stderr are set to the provided streams,
// Stdin is initialized to an empty reader, and Env is initialized to
// the environment of the current process.
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

type pendingFlush struct {
	s State
	f Flusher
	c []io.Closer

	wg sync.WaitGroup
	wf []*pendingFlush
}

func (pf *pendingFlush) closeWhenDone(c io.Closer) {
	pf.c = append(pf.c, c)
}

func (pf *pendingFlush) waitFor(other *pendingFlush) {
	pf.wg.Add(1)
	other.wf = append(other.wf, pf)
}

func (pf *pendingFlush) wait() {
	pf.wg.Wait()
}

func (pf *pendingFlush) done() {
	for _, c := range pf.c {
		c.Close()
	}
	for _, wf := range pf.wf {
		wf.wg.Done()
	}
}

// AddFlusher adds f to be flushed concurrently by FlushAll once the
// whole pipe finishes running.
func (s *State) AddFlusher(f Flusher) error {
	pf := &pendingFlush{s: *s, f: f}
	pf.s.Env = append([]string(nil), s.Env...)
	s.pendingFlushes = append(s.pendingFlushes, pf)
	return nil
}

// FlushAll flushes all pending flushers registered via AddFlusher.
func (s *State) FlushAll() error {
	done := make(chan error, len(s.pendingFlushes))
	for _, f := range s.pendingFlushes {
		go func(pf *pendingFlush) {
			pf.wait()
			err := pf.f.Flush(&pf.s)
			pf.done()
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

// Path returns the provided path relative to the state's current directory.
// If multiple arguments are provided, they're joined via filepath.Join.
// If path is absolute, it is taken by itself.
func (s *State) Path(path ...string) string {
	if len(path) == 0 {
		return s.Dir
	}
	if filepath.IsAbs(path[0]) {
		return filepath.Join(path...)
	}
	if len(path) == 1 {
		return filepath.Join(s.Dir, path[0])
	}
	return filepath.Join(append([]string{s.Dir}, path...)...)
}

func firstErr(err1, err2 error) error {
	if err1 != nil {
		return err1
	}
	return err2
}

// Run runs the p pipe discarding its output.
//
// See functions Output, CombinedOutput, and DisjointOutput.
func Run(p Pipe) error {
	s := NewState(nil, nil)
	err := p(s)
	if err == nil {
		err = s.FlushAll()
	}
	return err
}

// Output runs the p pipe and returns its stdout output.
//
// See functions Run, CombinedOutput, and DisjointOutput.
func Output(p Pipe) ([]byte, error) {
	outb := &OutputBuffer{}
	s := NewState(outb, nil)
	err := p(s)
	if err == nil {
		err = s.FlushAll()
	}
	return outb.Bytes(), err
}

// CombinedOutput runs the p pipe and returns its stdout and stderr
// outputs merged together.
//
// See functions Run, Output, and DisjointOutput.
func CombinedOutput(p Pipe) ([]byte, error) {
	outb := &OutputBuffer{}
	s := NewState(outb, outb)
	err := p(s)
	if err == nil {
		err = s.FlushAll()
	}
	return outb.Bytes(), err
}

// DisjointOutput runs the p pipe and returns its stdout and stderr outputs.
//
// See functions Run, Output, and CombinedOutput..
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

// OutputBuffer is a concurrency safe writer that buffers all input.
//
// It is used in the implementation of the output functions.
type OutputBuffer struct {
	m   sync.Mutex
	buf []byte
}

// Writes appends b to out's buffered data.
func (out *OutputBuffer) Write(b []byte) (n int, err error) {
	out.m.Lock()
	out.buf = append(out.buf, b...)
	out.m.Unlock()
	return len(b), nil
}

// Bytes returns all the data written to out.
func (out *OutputBuffer) Bytes() []byte {
	out.m.Lock()
	buf := out.buf
	out.m.Unlock()
	return buf
}

// Exec returns a pipe that runs the named program with the given arguments.
func Exec(name string, args ...string) Pipe {
	return func(s *State) error {
		s.AddFlusher(&execFlusher{name, args, make(chan *os.Process, 1)})
		return nil
	}
}

// System returns a pipe that runs cmd via a system shell.
// It is equivalent to the pipe Exec("/bin/sh", "-c", cmd).
func System(cmd string) Pipe {
	return Exec("/bin/sh", "-c", cmd)
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
	if p := <-f.ch; p != nil {
		p.Kill()
	}
}

// ChDir changes the pipe's current directory. If dir is relative,
// the change is made relative to the pipe's previous current directory.
//
// Other than it being the default current directory for new pipes,
// the working directory of the running process isn't considered or
// changed.
func ChDir(dir string) Pipe {
	return func(s *State) error {
		s.Dir = s.Path(dir)
		return nil
	}
}

// MkDir creates dir with the provided perm bits. If dir is relative,
// the created path is relative to the pipe's current directory.
func MkDir(dir string, perm os.FileMode) Pipe {
	return func(s *State) error {
		return os.Mkdir(s.Path(dir), perm)
	}
}

// SetEnvVar sets the value of the named environment variable in the pipe.
//
// Other than it being the default for new pipes, the environment of the
// running process isn't consulted or changed.
func SetEnvVar(name string, value string) Pipe {
	return func(s *State) error {
		s.SetEnvVar(name, value)
		return nil
	}
}

// CombineToErr modifes the stdout stream in the pipe so it is the same
// as the stderr stream. As a consequence, all further stdout output
// will be written to the stderr stream.
func CombineToErr() Pipe {
	return func(s *State) error {
		s.Stdout = s.Stderr
		return nil
	}
}

// CombineToOut modifes the stderr stream in the pipe so it is the same
// as the stdout stream. As a consequence, all further stderr output
// will be written to the stdout stream.
func CombineToOut() Pipe {
	return func(s *State) error {
		s.Stderr = s.Stdout
		return nil
	}
}

// Line creates a pipeline with the provided entries. The stdout of entry
// N in the pipeline is connected to the stdin of entry N+1.
// Entries are run sequentially, but flushed concurrently.
func Line(p ...Pipe) Pipe {
	return func(s *State) error {
		dir := s.Dir
		env := s.Env
		s.Env = append([]string(nil), s.Env...)
		defer func() {
			s.Dir = dir
			s.Env = env
		}()

		end := len(p) - 1
		endStdout := s.Stdout
		var r *io.PipeReader
		var w *io.PipeWriter
		for i, p := range p {
			var closeIn, closeOut *refCloser
			if r != nil {
				closeIn = &refCloser{r, 1}
			}
			if i == end {
				r, w = nil, nil
				s.Stdout = endStdout
			} else {
				r, w = io.Pipe()
				s.Stdout = w
				closeOut = &refCloser{w, 1}
			}

			oldLen := len(s.pendingFlushes)
			if err := p(s); err != nil {
				closeIn.Close()
				return err
			}
			newLen := len(s.pendingFlushes)

			for fi := oldLen; fi < newLen; fi++ {
				pf := s.pendingFlushes[fi]
				if c, ok := pf.s.Stdin.(io.Closer); ok && closeIn.uses(c) {
					closeIn.refs++
					pf.closeWhenDone(closeIn)
				}
				if c, ok := pf.s.Stdout.(io.Closer); ok && closeOut.uses(c) {
					closeOut.refs++
					pf.closeWhenDone(closeOut)
				}
				if c, ok := pf.s.Stderr.(io.Closer); ok && closeOut.uses(c) {
					closeOut.refs++
					pf.closeWhenDone(closeOut)
				}
			}
			closeIn.Close()
			closeOut.Close()

			if i < end {
				s.Stdin = r
			}
		}
		return nil
	}
}

type refCloser struct {
	c    io.Closer
	refs int32
}

func (rc *refCloser) uses(c io.Closer) bool {
	return rc != nil && rc.c == c
}

func (rc *refCloser) Close() error {
	if rc != nil && atomic.AddInt32(&rc.refs, -1) == 0 {
		return rc.c.Close()
	}
	return nil
}

// Script creates a pipe sequence with the provided entries.
// Entries are both run and flushed sequentially.
func Script(p ...Pipe) Pipe {
	return func(s *State) error {
		dir := s.Dir
		env := s.Env
		s.Env = append([]string(nil), s.Env...)
		defer func() {
			s.Dir = dir
			s.Env = env
		}()
		startLen := len(s.pendingFlushes)
		for _, p := range p {
			oldLen := len(s.pendingFlushes)
			if err := p(s); err != nil {
				return err
			}
			newLen := len(s.pendingFlushes)

			for fi := oldLen; fi < newLen; fi++ {
				for wi := startLen; wi < oldLen; wi++ {
					s.pendingFlushes[fi].waitFor(s.pendingFlushes[wi])
				}
			}
		}
		return nil
	}
}

type flushFunc func(s *State) error

func (f flushFunc) Flush(s *State) error { return f(s) }
func (f flushFunc) Kill()                {}

// FlushFunc is a helper to define a Pipe that adds a Flusher
// with f as its Flush method.
func FlushFunc(f func(s *State) error) Pipe {
	return func(s *State) error {
		s.AddFlusher(flushFunc(f))
		return nil
	}
}

// Echo writes str to the pipe's stdout.
func Echo(str string) Pipe {
	return FlushFunc(func(s *State) error {
		_, err := s.Stdout.Write([]byte(str))
		return err
	})
}

// Read reads data from r and writes it to the pipe's stdout.
func Read(r io.Reader) Pipe {
	return FlushFunc(func(s *State) error {
		_, err := io.Copy(s.Stdout, r)
		return err
	})
}

// Write writes to w the data read from the pipe's stdin.
func Write(w io.Writer) Pipe {
	return FlushFunc(func(s *State) error {
		_, err := io.Copy(w, s.Stdin)
		return err
	})
}

// Discard reads data from the pipe's stdin and discards it.
func Discard() Pipe {
	return Write(ioutil.Discard)
}

// Tee reads data from the pipe's stdin and writes it both to
// the pipe's stdout and to w.
func Tee(w io.Writer) Pipe {
	return FlushFunc(func(s *State) error {
		_, err := io.Copy(w, io.TeeReader(s.Stdin, s.Stdout))
		return err
	})
}

// ReadFile reads data from the file at path and writes it to the
// pipe's stdout.
func ReadFile(path string) Pipe {
	return FlushFunc(func(s *State) error {
		file, err := os.Open(s.Path(path))
		if err != nil {
			return err
		}
		_, err = io.Copy(s.Stdout, file)
		file.Close()
		return err
	})
}

// WriteFile writes to the file at path the data read from the
// pipe's stdin. If the file doesn't exist, it is created with perm.
func WriteFile(path string, perm os.FileMode) Pipe {
	return FlushFunc(func(s *State) error {
		file, err := os.OpenFile(s.Path(path), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
		if err != nil {
			return err
		}
		_, err = io.Copy(file, s.Stdin)
		return firstErr(err, file.Close())
	})
}

// TeeFile reads data from the pipe's stdin and writes it both to
// the pipe's stdout and to the file at path. If the file doesn't
// exist, it is created with perm.
func TeeFile(path string, perm os.FileMode) Pipe {
	return FlushFunc(func(s *State) error {
		file, err := os.OpenFile(s.Path(path), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
		if err != nil {
			return err
		}
		_, err = io.Copy(file, io.TeeReader(s.Stdin, s.Stdout))
		return firstErr(err, file.Close())
	})
}

// Filter filters lines read from the pipe's stdin so that only those
// for which f is true are written to the pipe's stdout.
// The line provided to f has '\n' and '\r' trimmed.
func Filter(f func(line string) bool) Pipe {
	return FlushFunc(func(s *State) error {
		r := bufio.NewReader(s.Stdin)
		for {
			line, err := r.ReadBytes('\n')
			eof := err == io.EOF
			if eof {
				if len(line) == 0 {
					return nil
				}
			} else if err != nil {
				return err
			}
			if f(string(bytes.TrimRight(line, "\r\n"))) {
				_, err := s.Stdout.Write(line)
				if err != nil {
					return err
				}
			}
			if eof {
				return nil
			}
		}
	})
}
