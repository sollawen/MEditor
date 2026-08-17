package finder

import (
	"reflect"
	"testing"
)

func TestParsePorcelain(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		input  []byte
		want   map[string]rune
		branch string
		state  dirState
	}{
		{
			name:   "empty",
			input:  []byte{},
			want:   map[string]rune{},
			branch: "",
		},
		{
			name:   "single modified file",
			input:  []byte(" M main.go\x00"),
			want:   map[string]rune{"main.go": 'M'},
			branch: "",
		},
		{
			name:   "untracked file",
			input:  []byte("?? newfile.txt\x00"),
			want:   map[string]rune{"newfile.txt": 'U'},
			branch: "",
		},
		{
			name:   "staged added file",
			input:  []byte("A  test.go\x00"),
			want:   map[string]rune{"test.go": 'A'},
			branch: "",
		},
		{
			name:   "deleted file",
			input:  []byte(" D old.go\x00"),
			want:   map[string]rune{"old.go": 'D'},
			branch: "",
		},
		{
			name:   "renamed file",
			input:  []byte("R  oldname.go\x00"),
			want:   map[string]rune{"oldname.go": 'R'},
			branch: "",
		},
		{
			name:   "multiple files with spaces",
			input:  []byte(" M my file.go\x00?? file with spaces.txt\x00"),
			want:   map[string]rune{"my file.go": 'M', "file with spaces.txt": 'U'},
			branch: "",
		},
		{
			name:   "Chinese filename",
			input:  []byte("?? 中文文件.txt\x00 M 我的项目.go\x00"),
			want:   map[string]rune{"中文文件.txt": 'U', "我的项目.go": 'M'},
			branch: "",
		},
		{
			name:   "subdirectory propagation (browsing sub/)",
			prefix: "sub/",
			input:  []byte(" M sub/dir/file.go\x00"),
			want:   map[string]rune{"dir": 'M'},
			branch: "",
		},
		{
			name:   "MM (staged and modified)",
			input:  []byte("MM both.go\x00"),
			want:   map[string]rune{"both.go": 'M'},
			branch: "",
		},
		{
			name:   "AM (added in index, modified in worktree)",
			input:  []byte("AM mixed.go\x00"),
			want:   map[string]rune{"mixed.go": 'M'}, // M 优先级更高
			branch: "",
		},
		{
			name:   "ignored short record",
			input:  []byte("M\x00"),
			want:   map[string]rune{},
			branch: "",
		},
		{
			name:   "complex mixed output",
			input:  []byte("M  modified.go\x00?? untracked.md\x00A  added.go\x00 D deleted.go\x00"),
			want:   map[string]rune{"modified.go": 'M', "untracked.md": 'U', "added.go": 'A', "deleted.go": 'D'},
			branch: "",
		},
		{
			name:   "root view three levels deep",
			input:  []byte(" M a/b/c/deep.go\x00"),
			want:   map[string]rune{"a": 'M'},
			branch: "",
		},
		{
			name:   "subdirectory view (internal/)",
			prefix: "internal/",
			input:  []byte(" M internal/action/a.go\x00"),
			want:   map[string]rune{"action": 'M'},
			branch: "",
		},
		{
			name:   "file directly in subdirectory (internal/flat.go)",
			prefix: "internal/",
			input:  []byte(" M internal/flat.go\x00"),
			want:   map[string]rune{"flat.go": 'M'},
			branch: "",
		},
		{
			name:   "multi-status aggregation in root view (M wins over A)",
			input:  []byte("A  x/added.go\x00 M x/mod.go\x00"),
			want:   map[string]rune{"x": 'M'},
			branch: "",
		},
		{
			name:   "backslash filename not mis-split (Unix \\ is valid char)",
			input:  []byte(" M a\\b.go\x00"),
			want:   map[string]rune{"a\\b.go": 'M'},
			branch: "",
		},
		// —— 分支头捕获（-b 产生的 "## " 首记录）——
		{
			name:   "branch header normal",
			input:  []byte("## main\x00 M main.go\x00"),
			want:   map[string]rune{"main.go": 'M'},
			branch: "main",
		},
		{
			name:   "branch with upstream (main...origin/main)",
			input:  []byte("## main...origin/main\x00 M a\x00"),
			want:   map[string]rune{"a": 'M'},
			branch: "main",
		},
		{
			name:   "branch with ahead/behind suffix",
			input:  []byte("## main [ahead 1]\x00"),
			want:   map[string]rune{},
			branch: "main",
		},
		{
			name:   "detached HEAD → empty branch",
			input:  []byte("## HEAD (no branch)\x00?? a\x00"),
			want:   map[string]rune{"a": 'U'},
			branch: "",
		},
		{
			name:   "unborn branch (No commits yet on main) → main",
			input:  []byte("## No commits yet on main\x00?? a\x00"),
			want:   map[string]rune{"a": 'U'},
			branch: "main",
		},
		// —— ignored（!!）状态字符（fix "I 冒泡" bug）——
		{
			name:   "ignored file in subdir → no bubble (fix)",
			input:  []byte("!! build/artifact.o\x00"),
			want:   map[string]rune{}, // 深处单文件不冒泡到父目录
			branch: "",
			state:  dirNormal,
		},
		{
			name:   "ignored single file in current dir → I on file",
			input:  []byte("!! secret.md\x00"),
			want:   map[string]rune{"secret.md": 'I'},
			branch: "",
			state:  dirNormal,
		},
		{
			name:   "ignored direct subdir (node_modules/) → I on name",
			input:  []byte("!! node_modules/\x00"),
			want:   map[string]rune{"node_modules": 'I'},
			branch: "",
			state:  dirNormal,
		},
		{
			name:   "ignored deep subdir (aaa/bbb/ccc/) from bbb/ level → I on ccc",
			prefix: "aaa/bbb/",
			input:  []byte("!! aaa/bbb/ccc/\x00"),
			want:   map[string]rune{"ccc": 'I'},
			branch: "",
			state:  dirNormal,
		},
		{
			name:   "ignored deep subdir from root → no bubble (too deep)",
			prefix: "aaa/",
			input:  []byte("!! aaa/bbb/ccc/\x00"),
			want:   map[string]rune{},
			branch: "",
			state:  dirNormal,
		},
		{
			name:   "browsing into ignored dir itself → dirAllIgnored",
			prefix: "aaa/bbb/ccc/",
			input:  []byte("!! aaa/bbb/ccc/\x00"),
			want:   map[string]rune{},
			branch: "",
			state:  dirAllIgnored, // rel==""：当前目录本身被 ignore
		},
		{
			name:   "M + ignored file in subdir → M not polluted by I bubble",
			input:  []byte(" M x/mod.go\x00!! x/ignored.log\x00"),
			want:   map[string]rune{"x": 'M'}, // ignored 深处单文件被丢弃
			branch: "",
			state:  dirNormal,
		},
		{
			name:   "ignored file deep in subtree from root → no bubble",
			prefix: "aaa/",
			input:  []byte("!! aaa/bbb/ccc/secret.md\x00"),
			want:   map[string]rune{},
			branch: "",
			state:  dirNormal,
		},
		{
			name:   "ignored file deep in subtree from immediate parent → I on file",
			prefix: "aaa/bbb/ccc/",
			input:  []byte("!! aaa/bbb/ccc/secret.md\x00"),
			want:   map[string]rune{"secret.md": 'I'},
			branch: "",
			state:  dirNormal,
		},
		// —— untracked（??）「整目录折叠」修复（fix "U 标志缺失" bug） ——
		// cwd 在仓库根，git 折叠 bigdir/ → 冒泡到顶层目录名，state=dirNormal（原有逻辑不变）
		{
			name:   "untracked dir from root to bubbles to top-level name",
			input:  []byte("?? bigdir/\x00"),
			prefix: "",
			want:   map[string]rune{"bigdir": 'U'},
			branch: "",
			state:  dirNormal,
		},
		// cwd 在 bigdir/，git 折叠 bigdir/（路径恒等于 cwd）→ dirAllUntracked，chars 为空
		{
			name:   "browsing into untracked dir itself dirAllUntracked",
			input:  []byte("?? bigdir/\x00"),
			prefix: "bigdir/",
			want:   map[string]rune{},
			branch: "",
			state:  dirAllUntracked,
		},
		// cwd 在 bigdir/sub/，git 折叠 bigdir/sub/（cwd 自己）→ dirAllUntracked，chars 为空
		{
			name:   "browsing into deep untracked dir sub dirAllUntracked",
			input:  []byte("?? bigdir/sub/\x00"),
			prefix: "bigdir/sub/",
			want:   map[string]rune{},
			branch: "",
			state:  dirAllUntracked,
		},
		// cwd 在仓库根但 git 逐个列出（目录内有 staged 文件所以不折叠）：
		// "?? bigdir/sub/b.txt" → rel = "bigdir/sub/b.txt"（含 /）→ 取 "bigdir" 冒泡，
		// state=dirNormal。这验证了「非折叠 untracked 走原冒泡路径、不误触 dirAllUntracked」。
		{
			name:   "non-collapsed untracked mixed staged untracked from root bubbles no dirAllUntracked",
			input:  []byte("?? bigdir/sub/b.txt\x00"),
			prefix: "",
			want:   map[string]rune{"bigdir": 'U'},
			branch: "",
			state:  dirNormal,
		},
		// —— 混合形态：?? 折叠目录 + 内部 !! 逐文件 ——
		// --ignored=traditional 下，git 对整体 untracked 的目录折叠报 "?? dir/"，
		// 但目录内被 ignore 的文件仍逐个列出。chars 保留这些 'I' 项，
		// fetchGit 据此给个别文件打 I、其余兜底 U。
		{
			name:   "untracked dir containing ignored files (mixed ?? + !!)",
			input:  []byte("?? gamePack/texas/audio/\x00!! gamePack/texas/audio/__pycache__/\x00!! gamePack/texas/audio/test_stt.py\x00"),
			prefix: "gamePack/texas/audio/",
			want:   map[string]rune{"__pycache__": 'I', "test_stt.py": 'I'},
			branch: "",
			state:  dirAllUntracked,
		},
		// —— ignored（!!）「深处」bug 修复 ——
		// ignored 的折叠路径是「ignored 树根」，不是 cwd 自己。
		// cwd 进实体深处时，git 仍报 "!! <树根>/"，prefix 是 path 的祖先，
		// 统一判据（HasPrefix(prefix, path)）捕获这种情况，不误算成孤儿。
		// igtest 场景：igroot/ 整 ignored，含 igroot/mid/deep/f3.txt
		{
			name:   "ignored deep dir cwd igroot mid dirAllIgnored",
			input:  []byte("!! igroot/\x00"),
			prefix: "igroot/mid/",
			want:   map[string]rune{},
			branch: "",
			state:  dirAllIgnored,
		},
		{
			name:   "ignored deep dir cwd igroot mid deep dirAllIgnored",
			input:  []byte("!! igroot/\x00"),
			prefix: "igroot/mid/deep/",
			want:   map[string]rune{},
			branch: "",
			state:  dirAllIgnored,
		},
		// .ruff_cache/ 整目录 ignored，cwd=.ruff_cache/0.12.5/
		{
			name:   "ignored real dir dot ruff_cache cwd dot ruff_cache 0.12.5 dirAllIgnored",
			input:  []byte("!! .ruff_cache/\x00"),
			prefix: ".ruff_cache/0.12.5/",
			want:   map[string]rune{},
			branch: "",
			state:  dirAllIgnored,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chars, branch, state := parsePorcelain(tt.input, tt.prefix)
			if !reflect.DeepEqual(chars, tt.want) {
				t.Errorf("chars = %#v, want %#v", chars, tt.want)
			}
			if branch != tt.branch {
				t.Errorf("branch = %q, want %q", branch, tt.branch)
			}
			if state != tt.state {
				t.Errorf("state = %v, want %v", state, tt.state)
			}
		})
	}
}
