package pipe_test

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"labix.org/v2/pipe"
	. "launchpad.net/gocheck"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func Test(t *testing.T) {
	TestingT(t)
}

type S struct{}

var _ = Suite(S{})

func (S) TestStatePath(c *C) {
	s := pipe.NewState(nil, nil)
	s.Dir = "/a"
	tests := []struct {
		path   []string
		result string
	}{
		{[]string{}, "/a"},
		{[]string{""}, "/a"},
		{[]string{"b"}, "/a/b"},
		{[]string{"b", "c"}, "/a/b/c"},
		{[]string{"/b", "c"}, "/b/c"},
	}
	for _, t := range tests {
		c.Assert(s.Path(t.path...), Equals, t.result)
	}
}

func (S) TestExecRun(c *C) {
	path := filepath.Join(c.MkDir(), "file")
	p := pipe.Exec("/bin/sh", "-c", "echo hello > "+path)
	err := pipe.Run(p)
	c.Assert(err, IsNil)

	data, err := ioutil.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, "hello\n")
}

func (S) TestExecOutput(c *C) {
	p := pipe.Exec("/bin/sh", "-c", "echo out1; echo err1 1>&2; echo out2; echo err2 1>&2")
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "out1\nout2\n")
}

func (S) TestExecCombinedOutput(c *C) {
	p := pipe.Exec("/bin/sh", "-c", "echo out1; echo err1 1>&2; echo out2; echo err2 1>&2")
	output, err := pipe.CombinedOutput(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "out1\nerr1\nout2\nerr2\n")
}

func (S) TestExecDividedOutput(c *C) {
	p := pipe.Exec("/bin/sh", "-c", "echo out1; echo err1 1>&2; echo out2; echo err2 1>&2")
	stdout, stderr, err := pipe.DividedOutput(p)
	c.Assert(err, IsNil)
	c.Assert(string(stdout), Equals, "out1\nout2\n")
	c.Assert(string(stderr), Equals, "err1\nerr2\n")
}

func (S) TestSystem(c *C) {
	p := pipe.System("echo out1; echo err1 1>&2; echo out2; echo err2 1>&2")
	stdout, stderr, err := pipe.DividedOutput(p)
	c.Assert(err, IsNil)
	c.Assert(string(stdout), Equals, "out1\nout2\n")
	c.Assert(string(stderr), Equals, "err1\nerr2\n")
}

func (S) TestLine(c *C) {
	p := pipe.Line(
		pipe.Exec("/bin/sh", "-c", "echo out1; echo err1 1>&2; echo out2; echo err2 1>&2"),
		pipe.Exec("sed", `s/\(...\)\([12]\)/\1-\2/`),
	)
	output, err := pipe.CombinedOutput(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "err1\nerr2\nout-1\nout-2\n")
}

func (S) TestLineTermination(c *C) {
	// Shouldn't block waiting for a reader that won't read.
	var b []byte
	for i := 0; i < 256*1024/8; i++ {
		b = append(b, "xxxxxxxx"...)
	}
	p := pipe.Line(
		pipe.Print(string(b)),
		pipe.Exec("true"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, ErrorMatches, `command "true": write \|1: broken pipe`)
	c.Assert(string(output), Equals, "")
}

func (S) TestScriptOutput(c *C) {
	p := pipe.Script(
		pipe.System("echo out1; echo err1 1>&2; echo out2; echo err2 1>&2"),
		pipe.System("echo out3; echo err3 1>&2; echo out4; echo err4 1>&2"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "out1\nout2\nout3\nout4\n")

}

func (S) TestScriptCombinedOutput(c *C) {
	p := pipe.Script(
		pipe.System("echo out1; echo err1 1>&2; echo out2; echo err2 1>&2"),
		pipe.System("echo out3; echo err3 1>&2; echo out4; echo err4 1>&2"),
	)
	output, err := pipe.CombinedOutput(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "out1\nerr1\nout2\nerr2\nout3\nerr3\nout4\nerr4\n")
}

func (S) TestErrorHandling(c *C) {
	sync := make(chan bool)
	p := pipe.Script(
		pipe.Line(
			pipe.TaskFunc(func(*pipe.State) error {
				sync <- true
				return fmt.Errorf("err1")
			}),
			pipe.TaskFunc(func(*pipe.State) error {
				<-sync
				return fmt.Errorf("err2")
			}),
		),
		pipe.Print("never happened"),
	)
	output, err := pipe.Output(p)
	if err.Error() != "err1; err2" && err.Error() != "err2; err1" {
		c.Fatalf(`want "err1; err2" or "err2; err1"; got %q`, err.Error())
	}
	c.Assert(string(output), Equals, "")
}

func (S) TestSetEnvVar(c *C) {
	os.Setenv("PIPE_NEW_VAR", "")
	os.Setenv("PIPE_OLD_VAR", "old")
	defer os.Setenv("PIPE_OLD_VAR", "")
	p := pipe.Script(
		pipe.SetEnvVar("PIPE_NEW_VAR", "new"),
		pipe.System("echo $PIPE_OLD_VAR $PIPE_NEW_VAR"),
		pipe.SetEnvVar("PIPE_NEW_VAR", "after"),
		func(s *pipe.State) error {
			count := 0
			prefix := "PIPE_NEW_VAR="
			for _, kv := range s.Env {
				if strings.HasPrefix(kv, prefix) {
					count++
				}
			}
			if count != 1 {
				return fmt.Errorf("found %d environment variables", count)
			}
			return nil
		},
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "old new\n")
	c.Assert(os.Getenv("PIPE_NEW_VAR"), Equals, "")
}

func (S) TestScriptIsolatesEnv(c *C) {
	p := pipe.Script(
		pipe.SetEnvVar("PIPE_VAR", "outer"),
		pipe.Script(
			pipe.SetEnvVar("PIPE_VAR", "inner"),
		),
		pipe.System("echo $PIPE_VAR"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "outer\n")
}

func (S) TestScriptIsolatesDir(c *C) {
	dir1 := c.MkDir()
	dir2 := c.MkDir()
	p := pipe.Script(
		pipe.ChDir(dir1),
		pipe.Script(
			pipe.ChDir(dir2),
		),
		pipe.System("echo $PWD"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, dir1+"\n")
}

func (S) TestLineIsolatesEnv(c *C) {
	p := pipe.Line(
		pipe.SetEnvVar("PIPE_VAR", "outer"),
		pipe.Line(
			pipe.SetEnvVar("PIPE_VAR", "inner"),
		),
		pipe.System("echo $PIPE_VAR"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "outer\n")
}

func (S) TestLineIsolatesDir(c *C) {
	dir1 := c.MkDir()
	dir2 := c.MkDir()
	p := pipe.Line(
		pipe.ChDir(dir1),
		pipe.Line(
			pipe.ChDir(dir2),
		),
		pipe.System("echo $PWD"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, dir1+"\n")
}

func (S) TestLineNesting(c *C) {
	b := &bytes.Buffer{}
	p := pipe.Line(
		pipe.Print("hello"),
		pipe.Line(
			pipe.Filter(func(line []byte) bool { return true }),
			pipe.Exec("sed", "s/l/k/g"),
		),
		pipe.Write(b),
	)
	err := pipe.Run(p)
	c.Assert(err, IsNil)
	c.Assert(b.String(), Equals, "hekko")
}

func (S) TestScriptNesting(c *C) {
	b := &bytes.Buffer{}
	p := pipe.Line(
		pipe.Print("hello"),
		pipe.Script(
			pipe.Print("world"),
			pipe.Exec("sed", "s/l/k/g"),
		),
		pipe.Write(b),
	)
	err := pipe.Run(p)
	c.Assert(err, IsNil)
	c.Assert(b.String(), Equals, "worldhekko")
}

func (S) TestScriptPreservesStreams(c *C) {
	p := pipe.Script(
		pipe.Line(
			pipe.Print("hello\n"),
			pipe.Discard(),
		),
		pipe.Exec("echo", "world"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "world\n")
}

func (S) TestChDir(c *C) {
	wd1, err := os.Getwd()
	c.Assert(err, IsNil)

	dir := c.MkDir()
	subdir := filepath.Join(dir, "subdir")
	err = os.Mkdir(subdir, 0755)
	p := pipe.Script(
		pipe.ChDir(dir),
		pipe.ChDir("subdir"),
		pipe.System("echo $PWD"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, subdir+"\n")

	wd2, err := os.Getwd()
	c.Assert(err, IsNil)
	c.Assert(wd2, Equals, wd1)
}

func (S) TestMkDir(c *C) {
	dir := c.MkDir()
	subdir := filepath.Join(dir, "subdir")
	subsubdir := filepath.Join(subdir, "subsubdir")
	p := pipe.Script(
		pipe.MkDir(subdir, 0755),
		pipe.ChDir(subdir),
		pipe.MkDir("subsubdir", 0700),
		pipe.ChDir("subsubdir"),
		pipe.System("echo $PWD"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, subsubdir+"\n")

	stat, err := os.Stat(subsubdir)
	c.Assert(err, IsNil)
	c.Assert(stat.Mode()&os.ModePerm, Equals, os.FileMode(0700))
}

func (S) TestPrint(c *C) {
	p := pipe.Line(
		pipe.Print("hello:", 42),
		pipe.Exec("sed", "s/l/k/g"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "hekko:42")
}

func (S) TestPrintln(c *C) {
	p := pipe.Line(
		pipe.Println("hello:", 42),
		pipe.Exec("sed", "s/l/k/g"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "hekko: 42\n")
}

func (S) TestPrintf(c *C) {
	p := pipe.Line(
		pipe.Printf("hello:%d", 42),
		pipe.Exec("sed", "s/l/k/g"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "hekko:42")
}

func (S) TestRead(c *C) {
	p := pipe.Line(
		pipe.Read(bytes.NewBufferString("hello")),
		pipe.Exec("sed", "s/l/k/g"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "hekko")
}

func (S) TestWrite(c *C) {
	var b bytes.Buffer
	p := pipe.Line(
		pipe.Print("hello"),
		pipe.Exec("sed", "s/l/k/g"),
		pipe.Write(&b),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "")
	c.Assert(b.String(), Equals, "hekko")
}

func (S) TestDiscard(c *C) {
	p := pipe.Line(
		pipe.Print("hello"),
		pipe.Discard(),
		pipe.Print("world"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "world")
}

func (S) TestTee(c *C) {
	var b bytes.Buffer
	p := pipe.Line(
		pipe.Print("hello"),
		pipe.Exec("sed", "s/l/k/g"),
		pipe.Tee(&b),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "hekko")
	c.Assert(b.String(), Equals, "hekko")
}

func (S) TestReadFileAbsolute(c *C) {
	dir := c.MkDir()
	path := filepath.Join(dir, "file")
	err := ioutil.WriteFile(path, []byte("hello"), 0644)
	c.Assert(err, IsNil)

	p := pipe.Line(
		pipe.ReadFile(path),
		pipe.Exec("sed", "s/l/k/g"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "hekko")
}

func (S) TestReadFileRelative(c *C) {
	dir := c.MkDir()
	path := filepath.Join(dir, "file")
	err := ioutil.WriteFile(path, []byte("hello"), 0644)
	c.Assert(err, IsNil)

	p := pipe.Line(
		pipe.ReadFile(path),
		pipe.Exec("sed", "s/l/k/g"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "hekko")
}

func (S) TestReadFileNonExistent(c *C) {
	path := filepath.Join(c.MkDir(), "file")
	p := pipe.Line(
		pipe.ReadFile(path),
		pipe.Exec("cat"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, ErrorMatches, "open .*/file: no such file or directory")
	c.Assert(output, IsNil)
}

func (S) TestWriteFileAbsolute(c *C) {
	path := filepath.Join(c.MkDir(), "file")
	p := pipe.Line(
		pipe.Print("hello"),
		pipe.Exec("sed", "s/l/k/g"),
		pipe.WriteFile(path, 0600),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "")

	data, err := ioutil.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, "hekko")
}

func (S) TestWriteFileRelative(c *C) {
	dir := c.MkDir()
	path := filepath.Join(dir, "file")
	p := pipe.Script(
		pipe.ChDir(dir),
		pipe.Line(
			pipe.Print("hello"),
			pipe.Exec("sed", "s/l/k/g"),
			pipe.WriteFile("file", 0600),
		),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "")

	data, err := ioutil.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, "hekko")
}

func (S) TestWriteFileMode(c *C) {
	path := filepath.Join(c.MkDir(), "file")
	p := pipe.WriteFile(path, 0600)
	_, err := pipe.Output(p)
	c.Assert(err, IsNil)

	stat, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(stat.Mode()&os.ModePerm, Equals, os.FileMode(0600))
}

func (S) TestAppendFileAbsolute(c *C) {
	path := filepath.Join(c.MkDir(), "file")
	p := pipe.Script(
		pipe.Line(
			pipe.Print("hello "),
			pipe.AppendFile(path, 0600),
		),
		pipe.Line(
			pipe.Print("world!"),
			pipe.AppendFile(path, 0600),
		),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "")

	data, err := ioutil.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, "hello world!")
}

func (S) TestAppendFileRelative(c *C) {
	dir := c.MkDir()
	path := filepath.Join(dir, "file")
	p := pipe.Script(
		pipe.ChDir(dir),
		pipe.Line(
			pipe.Print("hello "),
			pipe.AppendFile("file", 0600),
		),
		pipe.Line(
			pipe.Print("world!"),
			pipe.AppendFile("file", 0600),
		),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "")

	data, err := ioutil.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, "hello world!")
}

func (S) TestAppendFileMode(c *C) {
	path := filepath.Join(c.MkDir(), "file")
	p := pipe.AppendFile(path, 0600)
	_, err := pipe.Output(p)
	c.Assert(err, IsNil)

	stat, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(stat.Mode()&os.ModePerm, Equals, os.FileMode(0600))
}

func (S) TestTeeFileAbsolute(c *C) {
	path := filepath.Join(c.MkDir(), "file")
	p := pipe.Line(
		pipe.Print("hello"),
		pipe.Exec("sed", "s/l/k/g"),
		pipe.TeeFile(path, 0600),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "hekko")

	data, err := ioutil.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, "hekko")

	stat, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(stat.Mode()&os.ModePerm, Equals, os.FileMode(0600))
}

func (S) TestTeeFileRelative(c *C) {
	dir := c.MkDir()
	path := filepath.Join(dir, "file")
	p := pipe.Line(
		pipe.ChDir(dir),
		pipe.Print("hello"),
		pipe.Exec("sed", "s/l/k/g"),
		pipe.TeeFile("file", 0600),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "hekko")

	data, err := ioutil.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, "hekko")
}

func (S) TestTeeFileMode(c *C) {
	path := filepath.Join(c.MkDir(), "file")
	p := pipe.Line(
		pipe.Print("hello"),
		pipe.TeeFile(path, 0600),
	)
	err := pipe.Run(p)
	c.Assert(err, IsNil)

	stat, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(stat.Mode()&os.ModePerm, Equals, os.FileMode(0600))
}

func (S) TestFilter(c *C) {
	p := pipe.Line(
		pipe.System("echo out1; echo err1 1>&2; echo out2; echo err2 1>&2; echo out3"),
		pipe.Filter(func(line []byte) bool { return string(line) != "out2" }),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "out1\nout3\n")
}

func (S) TestFilterNoNewLine(c *C) {
	p := pipe.Line(
		pipe.Print("out1\nout2\nout3"),
		pipe.Filter(func(line []byte) bool { return string(line) != "out2" }),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "out1\nout3")
}

func (S) TestReplace(c *C) {
	p := pipe.Line(
		pipe.System("echo out1; echo err1 1>&2; echo out2; echo err2 1>&2; echo out3"),
		pipe.Replace(func(line []byte) []byte {
			if bytes.HasPrefix(line, []byte("out")) {
				if line[3] == '3' {
					return nil
				}
				return []byte{'l', line[3], ','}
			}
			return line
		}),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "l1,l2,")
}

func (S) TestReplaceNoNewLine(c *C) {
	p := pipe.Line(
		pipe.Print("out1\nout2\nout3"),
		pipe.Replace(func(line []byte) []byte {
			if bytes.HasPrefix(line, []byte("out")) {
				if line[3] == '2' {
					return nil
				}
				return []byte{'l', line[3], ','}
			}
			return line
		}),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "l1,l3,")
}

func (S) TestKillAbortedExecTask(c *C) {
	p := pipe.Script(
		pipe.TaskFunc(func(*pipe.State) error { return fmt.Errorf("boom") }),
		pipe.Exec("will-not-run"),
	)
	_, err := pipe.Output(p)
	c.Assert(err, ErrorMatches, "boom")
}

func (S) TestRenameFileAbsolute(c *C) {
	dir := c.MkDir()
	from := filepath.Join(dir, "from")
	to := filepath.Join(dir, "to")
	p := pipe.Script(
		pipe.WriteFile(from, 0644),
		pipe.RenameFile(from, to),
	)
	err := pipe.Run(p)
	c.Assert(err, IsNil)

	_, err = os.Stat(from)
	c.Assert(err, NotNil)
	_, err = os.Stat(to)
	c.Assert(err, IsNil)
}

func (S) TestRenameFileRelative(c *C) {
	dir := c.MkDir()
	from := filepath.Join(dir, "from")
	to := filepath.Join(dir, "to")
	p := pipe.Script(
		pipe.ChDir(dir),
		pipe.WriteFile("from", 0644),
		pipe.RenameFile("from", "to"),
	)
	err := pipe.Run(p)
	c.Assert(err, IsNil)

	_, err = os.Stat(from)
	c.Assert(err, NotNil)
	_, err = os.Stat(to)
	c.Assert(err, IsNil)
}
