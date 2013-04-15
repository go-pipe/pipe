package pipe_test

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"labix.org/v1/pipe"
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
	tests := []struct{ path []string; result string }{
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
	p := pipe.Exec("/bin/sh", "-c", "echo hello > " + path)
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

func (S) TestExecDisjointOutput(c *C) {
	p := pipe.Exec("/bin/sh", "-c", "echo out1; echo err1 1>&2; echo out2; echo err2 1>&2")
	stdout, stderr, err := pipe.DisjointOutput(p)
	c.Assert(err, IsNil)
	c.Assert(string(stdout), Equals, "out1\nout2\n")
	c.Assert(string(stderr), Equals, "err1\nerr2\n")
}

func (S) TestSystem(c *C) {
	p := pipe.System("echo out1; echo err1 1>&2; echo out2; echo err2 1>&2")
	stdout, stderr, err := pipe.DisjointOutput(p)
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
	for i := 0; i < 256 * 1024 / 8; i++ {
		b = append(b, "xxxxxxxx"...)
	}
	p := pipe.Line(
		pipe.Echo(string(b)),
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

func (S) TestCombineToErr(c *C) {
	p := pipe.Script(
		pipe.CombineToErr(),
		pipe.System("echo out1; echo err1 1>&2; echo out2; echo err2 1>&2"),
	)
	stdout, stderr, err := pipe.DisjointOutput(p)
	c.Assert(err, IsNil)
	c.Assert(string(stdout), Equals, "")
	c.Assert(string(stderr), Equals, "out1\nerr1\nout2\nerr2\n")
}

func (S) TestCombineToOut(c *C) {
	p := pipe.Script(
		pipe.CombineToOut(),
		pipe.System("echo out1; echo err1 1>&2; echo out2; echo err2 1>&2"),
	)
	stdout, stderr, err := pipe.DisjointOutput(p)
	c.Assert(err, IsNil)
	c.Assert(string(stdout), Equals, "out1\nerr1\nout2\nerr2\n")
	c.Assert(string(stderr), Equals, "")
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
	c.Assert(stat.Mode() & os.ModePerm, Equals, os.FileMode(0700))
}

func (S) TestEcho(c *C) {
	p := pipe.Line(
		pipe.Echo("hello"),
		pipe.Exec("sed", "s/l/k/g"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "hekko")
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
		pipe.Echo("hello"),
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
		pipe.Echo("hello"),
		pipe.Discard(),
		pipe.Echo("world"),
	)
	output, err := pipe.Output(p)
	c.Assert(err, IsNil)
	c.Assert(string(output), Equals, "world")
}

func (S) TestTee(c *C) {
	var b bytes.Buffer
	p := pipe.Line(
		pipe.Echo("hello"),
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
		pipe.Echo("hello"),
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
			pipe.Echo("hello"),
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
	c.Assert(stat.Mode() & os.ModePerm, Equals, os.FileMode(0600))
}

func (S) TestTeeFileAbsolute(c *C) {
	path := filepath.Join(c.MkDir(), "file")
	p := pipe.Line(
		pipe.Echo("hello"),
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
	c.Assert(stat.Mode() & os.ModePerm, Equals, os.FileMode(0600))
}

func (S) TestTeeFileRelative(c *C) {
	dir := c.MkDir()
	path := filepath.Join(dir, "file")
	p := pipe.Line(
		pipe.ChDir(dir),
		pipe.Echo("hello"),
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
		pipe.Echo("hello"),
		pipe.TeeFile(path, 0600),
	)
	err := pipe.Run(p)
	c.Assert(err, IsNil)

	stat, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(stat.Mode() & os.ModePerm, Equals, os.FileMode(0600))
}
