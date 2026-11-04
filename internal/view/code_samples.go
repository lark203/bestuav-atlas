package view

// 代码编辑器示例用到的示例文件（移植自 mygo examples/code-editor，MIT）。
//
// 五个文件覆盖五种语言，且都带 TODO 注释——示例把 TODO 标成诊断告警，
// 用来演示编辑器左侧的告警条与悬停提示。

var codeSamples = []struct{ name, text string }{
	{"server.go", `// Package server serves greetings over HTTP.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Server greets the names it is sent, and counts them.
type Server struct {
	mu     sync.Mutex
	counts map[string]int
	prefix string
}

// New returns a server greeting with prefix.
func New(prefix string) *Server {
	return &Server{counts: map[string]int{}, prefix: prefix}
}

// ServeHTTP greets the name in the query, as JSON.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.counts[name]++
	n := s.counts[name]
	s.mu.Unlock()
	// TODO: rate limit greetings per address.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"greeting": fmt.Sprintf("%s, %s!", s.prefix, name),
		"count":    n,
	})
}
`},
	{"App.tsx", `import { useEffect, useState } from "react";

type Todo = { id: number; title: string; done: boolean };

// TODO: persist todos in local storage.
export function App({ initial }: { initial: Todo[] }) {
  const [todos, setTodos] = useState<Todo[]>(initial);
  const [filter, setFilter] = useState<"all" | "open">("all");

  useEffect(() => {
    document.title = ` + "`${todos.filter((t) => !t.done).length} open`" + `;
  }, [todos]);

  const shown = filter === "all" ? todos : todos.filter((t) => !t.done);
  return (
    <ul className="todos">
      {shown.map((t) => (
        <li key={t.id} onClick={() => setTodos(toggle(todos, t.id))}>
          {t.title}
        </li>
      ))}
    </ul>
  );
}

function toggle(todos: Todo[], id: number): Todo[] {
  return todos.map((t) => (t.id === id ? { ...t, done: !t.done } : t));
}
`},
	{"style.css", `:root {
  --accent: #2563eb;
  --radius: 6px;
}

.todos {
  display: grid;
  gap: 8px;
  padding: 0;
}

.todos li:hover {
  background: color-mix(in oklab, var(--accent) 12%, white);
  border-radius: var(--radius);
}
`},
	{"fib.py", `"""Fibonacci numbers, three ways."""
from functools import cache


@cache
def fib(n: int) -> int:
    """The nth Fibonacci number, recursively."""
    if n < 2:
        return n
    return fib(n - 1) + fib(n - 2)


def fib_iter(n):
    a, b = 0, 1
    for _ in range(n):
        a, b = b, a + b
    return a


if __name__ == "__main__":
    # TODO: take n from the command line.
    print(f"fib(30) = {fib(30)}, {fib_iter(30)!r}")
`},
	{"README.md", "# 代码编辑器\n\n**mygo** 的 `ui/editor` 包示例。\n\n- 改一个文件，然后切到*变更*页看 diff。\n- 按 `Cmd/Ctrl+D` 选中下一个相同的词。\n\n```go\nst := &editor.State{Buffer: editor.NewBuffer(src)}\neditor.View(c, st).Fill()\n```\n"},
}
