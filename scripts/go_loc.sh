#!/usr/bin/env bash
# 统计 Go 代码行数（排除测试、注释、空行）
# 用法: ./scripts/go_loc.sh [目录] [--detail]

set -euo pipefail

DIR="${1:-.}"
DETAIL="${2:-}"

# 查找 Go 文件
all_files() { find "$DIR" -name '*.go'; }
src_files() { find "$DIR" -name '*.go' ! -name '*_test.go'; }
test_files() { find "$DIR" -name '*_test.go'; }

# 计算行数
count_lines() { xargs cat 2>/dev/null | wc -l; }
count_blank() { xargs cat 2>/dev/null | grep -c '^\s*$' || true; }
count_comment() { xargs cat 2>/dev/null | grep -cE '^\s*(//|/\*|\*)' || true; }
count_effective() {
    xargs cat 2>/dev/null \
        | grep -v '^\s*$' \
        | grep -v '^\s*//' \
        | grep -v '^\s*/\*' \
        | grep -v '^\s*\*' \
        | wc -l
}

total=$(all_files | count_lines)
test_lines=$(test_files | count_lines)
src_total=$(src_files | count_lines)
src_blank=$(src_files | count_blank)
src_comment=$(src_files | count_comment)
src_effective=$(src_files | count_effective)

echo "============================="
echo " Go 代码行数统计: $DIR"
echo "============================="
printf "%-20s %8d\n" "总行数（含测试）" "$total"
printf "%-20s %8d\n" "测试文件行数" "$test_lines"
printf "%-20s %8d\n" "非测试文件总行数" "$src_total"
printf "%-20s %8d\n" "  空行" "$src_blank"
printf "%-20s %8d\n" "  注释行" "$src_comment"
printf "%-20s %8d\n" "有效代码行数" "$src_effective"
echo "============================="

# --detail: 按目录细分
if [[ "$DETAIL" == "--detail" ]]; then
    echo ""
    echo "按目录细分（有效代码行数）:"
    echo "-----------------------------"
    find "$DIR" -name '*.go' ! -name '*_test.go' -printf '%h\n' \
        | sort -u \
        | while read -r d; do
            n=$(find "$d" -maxdepth 1 -name '*.go' ! -name '*_test.go' -print0 \
                | xargs -0 cat 2>/dev/null \
                | grep -v '^\s*$' \
                | grep -v '^\s*//' \
                | grep -v '^\s*/\*' \
                | grep -v '^\s*\*' \
                | wc -l)
            [[ "$n" -gt 0 ]] && printf "%8d  %s\n" "$n" "$d"
        done | sort -rn
    echo "-----------------------------"
fi
