#!/usr/bin/env bash
# skill-package：agent 手册必须按主流平台的 Agent Skill 约定组织，且仓库/打包/CLI 三处名字一致
#
# 约定（Claude Agent Skills 等平台通用）：一个技能一个目录，目录里放 SKILL.md，
# 文件头是 YAML frontmatter（name 必须与目录名一致，description 说明"什么时候该用它"，
# 平台就是靠这两项决定要不要加载这个技能）。
#
# 这个脚本盯的是**改名容易改漏**的地方：
#   - 仓库里的位置（skills/<name>/SKILL.md）
#   - 打包时放进归档的名字（build-release.sh）
#   - 升级器从归档里取的名字（internal/updater 的 skillsName）
# 三处只要有一处不同步，用户就会拿到"归档里有、但 upgrade --skills 说没有"的怪结果。
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0
ok() { echo "  ok: $1"; }
bad() { echo "FAIL: $1" >&2; fail=1; }

skill="skills/dtool/SKILL.md"

# 1) 布局：一个技能一个目录，里面是 SKILL.md
[[ -f "$skill" ]] || bad "缺少 $skill（Agent Skill 约定：<技能目录>/SKILL.md）"
[[ -e "skills.md" ]] && bad "根目录还有 skills.md：手册只能有一份，别复制成两份（改了这份、忘了那份）"

if [[ -f "$skill" ]]; then
  # 2) frontmatter：必须从第 1 行开始，闭合成对
  [[ "$(head -1 "$skill")" == "---" ]] || bad "$skill 第 1 行必须是 ---（frontmatter 起始）"
  closing="$(awk 'NR > 1 && /^---$/ { print NR; exit }' "$skill")"
  [[ -n "$closing" ]] || bad "$skill 缺少 frontmatter 结束的 ---"

  # name 必须与目录名一致（平台按目录名索引技能）
  dir="$(basename "$(dirname "$skill")")"
  name="$(awk 'NR > 1 && /^name:/ { sub(/^name:[[:space:]]*/, ""); print; exit }' "$skill")"
  [[ -n "$name" ]] || bad "$skill 的 frontmatter 缺少 name"
  [[ "$name" == "$dir" ]] || bad "name=$name 与目录名 $dir 不一致（平台按目录名索引）"
  if [[ -n "$name" ]] && ! grep -Eq '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$' <<<"$name"; then
    bad "name=$name 只能是「小写字母/数字/连字符」（平台对技能名的要求）"
  fi

  # description：必填、非空，且不能超过平台的 1024 字符上限
  desc="$(awk 'NR > 1 && /^description:/ { sub(/^description:[[:space:]]*/, ""); print; exit }' "$skill")"
  [[ -n "$desc" ]] || bad "$skill 的 frontmatter 缺少 description（决定平台何时加载它）"
  dlen="$(printf '%s' "$desc" | wc -m)"
  (( dlen <= 1024 )) || bad "description 有 $dlen 字符，超过 1024 上限"
  # description 得说清"什么时候用"，否则技能不会被触发——粗略但有效的最低要求
  grep -q '时使用\|当用户' <<<"$desc" || bad "description 应写明「什么时候该用它」（如「当用户…时使用」）"

  # 3) 正文不能是空壳（只有 frontmatter 的技能没有价值）
  if [[ -n "$closing" ]]; then
    body="$(tail -n +"$((closing + 1))" "$skill" | grep -cvE '^[[:space:]]*$' || true)"
    (( body >= 20 )) || bad "正文只有 $body 行非空内容，太少（技能正文应该是可用的操作手册）"
  fi
fi

# 4) 打包与取用必须同名：归档里叫什么，upgrade --skills 就从归档里找什么
grep -q 'cp skills/dtool/SKILL.md "${stage}/SKILL.md"' scripts/build-release.sh \
  || bad "build-release.sh 应把 $skill 复制成归档根目录的 SKILL.md"
grep -q 'skillsName = "SKILL.md"' internal/updater/updater.go \
  || bad "internal/updater 的 skillsName 应为 SKILL.md（必须与归档里的名字一致）"
grep -q 'legacySkillsName = "skills.md"' internal/updater/updater.go \
  || bad "应保留 legacySkillsName 回退（v0.2.0 及以前的归档里叫 skills.md）"

if (( fail )); then echo "skill-package: FAILED" >&2; exit 1; fi
echo "skill-package: OK（$skill 布局/frontmatter 合规，仓库·打包·升级器三处同名）"
