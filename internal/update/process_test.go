package update

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/childprocess"
)

func TestGoCommandCancellationWithDescendantPipes(t *testing.T) {
	dir := t.TempDir()
	source, ready := filepath.Join(dir, "main.go"), filepath.Join(dir, "ready")
	program := `package main
import("os";"os/exec";"time")
func main(){ child:=exec.Command("/bin/sleep","20");child.Stdout=os.Stdout;child.Stderr=os.Stderr;if err:=child.Start();err!=nil{panic(err)};if err:=os.WriteFile(os.Args[1],[]byte("ready"),0600);err!=nil{panic(err)};time.Sleep(20*time.Second);child.Wait() }
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, err := goCmd(ctx, "run", source, ready)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	done := make(chan error, 1)
	go func() { done <- childprocess.Run(cmd) }()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("before readiness: %v %s", err, &output)
		case <-time.After(10 * time.Millisecond):
		}
	}
	start := time.Now()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("go command survived cancellation")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("descendant delayed return: %s", elapsed)
	}
}
