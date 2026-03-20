#!/usr/bin/env bash
set -euo pipefail

YES=0
SIMULATE=0
SKIP_DEPS=0
INSTALL_CLAUDE=0
PREFLIGHT_ONLY=0
REUSE_GH_AUTH=1
REUSE_CLAUDE_AUTH=1
SHELL_INTEGRATION="none"
REMOVE_SHELL_INTEGRATION="none"
REMOVE_CRON=0
SCHEDULER_EXPLICIT=0
UPGRADE_HOME_REPO=0

APP_DIR_DEFAULT="$HOME/.ccclaw"
HOME_REPO_DEFAULT="/opt/ccclaw"
CONTROL_REPO_DEFAULT="41490/ccclaw"
BIN_LINK_DEFAULT="$HOME/.local/bin/ccclaw"
SYSTEMD_USER_DIR_DEFAULT="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
TASK_CLONE_ROOT_DEFAULT="/opt/src/3claw"
SCHEDULER_DEFAULT="auto"
CALENDAR_TIMEZONE_DEFAULT="Asia/Shanghai"
INGEST_CALENDAR_DEFAULT="*:0/5"
RUN_CALENDAR_DEFAULT="*:0/10"
PATROL_CALENDAR_DEFAULT="*:0/2"
JOURNAL_CALENDAR_DEFAULT="*-*-* 23:50:00"
DIST_DIR="$(cd "$(dirname "$0")" && pwd)"
BASHRC_FILE="${BASHRC_FILE:-$HOME/.bashrc}"
SHELL_BLOCK_BEGIN="# >>> ccclaw managed block >>>"
SHELL_BLOCK_END="# <<< ccclaw managed block <<<"

APP_DIR="${APP_DIR:-$APP_DIR_DEFAULT}"
HOME_REPO="${HOME_REPO:-$HOME_REPO_DEFAULT}"
HOME_REPO_MODE="${HOME_REPO_MODE:-init}"
HOME_REPO_REMOTE="${HOME_REPO_REMOTE:-}"
CCCLAW_VERSION="${CCCLAW_VERSION:-}"
CONTROL_REPO="$CONTROL_REPO_DEFAULT"
BIN_LINK="${BIN_LINK:-$BIN_LINK_DEFAULT}"
SYSTEMD_USER_DIR="$SYSTEMD_USER_DIR_DEFAULT"
TASK_CLONE_ROOT="${TASK_CLONE_ROOT:-$TASK_CLONE_ROOT_DEFAULT}"
SCHEDULER="${SCHEDULER:-$SCHEDULER_DEFAULT}"

TASK_REPO_MODE="${TASK_REPO_MODE:-none}"
TASK_REPO_REMOTE="${TASK_REPO_REMOTE:-}"
TASK_REPO_LOCAL="${TASK_REPO_LOCAL:-}"
TASK_REPO="${TASK_REPO:-}"
TASK_REPO_PATH="${TASK_REPO_PATH:-}"
TASK_KB_PATH="${TASK_KB_PATH:-}"
CALENDAR_TIMEZONE="${CALENDAR_TIMEZONE:-$CALENDAR_TIMEZONE_DEFAULT}"
INGEST_CALENDAR="${INGEST_CALENDAR:-$INGEST_CALENDAR_DEFAULT}"
RUN_CALENDAR="${RUN_CALENDAR:-$RUN_CALENDAR_DEFAULT}"
PATROL_CALENDAR="${PATROL_CALENDAR:-$PATROL_CALENDAR_DEFAULT}"
JOURNAL_CALENDAR="${JOURNAL_CALENDAR:-$JOURNAL_CALENDAR_DEFAULT}"

ENV_FILE=""
CONFIG_FILE=""
CRONCFG_FILE=""
VAR_DIR=""
LOG_DIR=""
KB_DIR=""
CLAUDE_WRAPPER=""
SCHEDULER_EFFECTIVE=""
SCHEDULER_REASON="未判定"
SYSTEMD_READY=0
SYSTEMD_CONTROL_READY=0
SYSTEMD_CHECK_STATUS="未探查"
SYSTEMD_CONTROL_STATUS="未探查"
GH_AUTH_STATUS="未探查"
GH_TOKEN_DETECTED=""
CLAUDE_AUTH_READY=0
CLAUDE_AUTH_METHOD="未探查"
GH_TOKEN_SOURCE="未写入"
ANTHROPIC_API_KEY_SOURCE="未写入"
ANTHROPIC_BASE_URL_SOURCE="未写入"
ANTHROPIC_AUTH_TOKEN_SOURCE="未写入"
GREPTILE_API_KEY_SOURCE="未写入"
SHELL_INTEGRATION_STATUS="disabled"
SHELL_INTEGRATION_REASON="默认关闭，未写入 shell 配置"
CRON_READY=0
CRON_CHECK_STATUS="未探查"
CRON_MANAGED_STATUS="未处理"
CRON_MANAGED_REASON="未执行受控 crontab 管理"
SYSTEMD_ACTIVATION_STATUS="未处理"
SYSTEMD_ACTIVATION_REASON="未执行 user systemd 激活"
LEGACY_RUN_CLEANUP_NOTE="未检查遗留 ccclaw-run unit"
CONFIG_FILE_ALREADY_EXISTS=0

expand_path() {
  local path="$1"
  if [[ -z "$path" ]]; then
    printf '\n'
    return 0
  fi
  if [[ "$path" == ~* ]]; then
    printf '%s\n' "${path/#\~/$HOME}"
  else
    printf '%s\n' "$path"
  fi
}

refresh_paths() {
  APP_DIR="$(expand_path "$APP_DIR")"
  HOME_REPO="$(expand_path "$HOME_REPO")"
  BIN_LINK="$(expand_path "$BIN_LINK")"
  BASHRC_FILE="$(expand_path "$BASHRC_FILE")"
  TASK_CLONE_ROOT="$(expand_path "$TASK_CLONE_ROOT")"
  TASK_REPO_LOCAL="$(expand_path "$TASK_REPO_LOCAL")"
  TASK_REPO_PATH="$(expand_path "$TASK_REPO_PATH")"
  TASK_KB_PATH="$(expand_path "$TASK_KB_PATH")"
  ENV_FILE="$APP_DIR/.env"
  CONFIG_FILE="$APP_DIR/ops/config/config.toml"
  CRONCFG_FILE="$APP_DIR/croncfg.md"
  VAR_DIR="$APP_DIR/var"
  LOG_DIR="$APP_DIR/log"
  KB_DIR="$HOME_REPO/kb"
  CLAUDE_WRAPPER="$APP_DIR/bin/ccclaude"
}

log() { printf '[ccclaw-install] %s\n' "$*"; }
warn() { printf '[ccclaw-install][WARN] %s\n' "$*" >&2; }
fail() { printf '[ccclaw-install][FAIL] %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<USAGE
用法: $(basename "$0") [选项]

选项:
  --yes                     非交互模式，尽量使用默认值
  --simulate                只打印安装流程与当前探查结果，不写入文件
  --preflight-only          只执行安装前体检，不落盘
  --skip-deps               跳过系统依赖安装
  --install-claude          非交互模式下自动执行 Claude 官方安装
  --scheduler MODE          调度器模式: auto|systemd|cron|none（安装器不再自动托管 cron）
  --task-clone-root PATH    remote 任务仓库本地 clone 根目录，默认 /opt/src/3claw
  --app-dir PATH            程序目录，默认 ~/.ccclaw
  --home-repo PATH          本体仓库目录，默认 /opt/ccclaw
  --home-repo-mode MODE     本体仓库模式: init|remote|local
  --home-repo-remote URL    本体远程仓库 URL 或 owner/repo；传入后自动切换 remote 模式
  --task-repo-mode MODE     任务仓库模式: none|remote|local
  --task-repo-remote URL    任务远程仓库 URL 或 owner/repo；传入后自动切换 remote 模式
  --task-repo-local PATH    本地已有任务仓库路径；传入后自动切换 local 模式
  --task-repo REPO          任务仓库 owner/repo；未传时尽量自动推断
  --task-repo-path PATH     任务仓库本地路径；remote 模式默认 /opt/src/3claw/owner/repo
  --task-repo-kb-path PATH  任务仓库对应 kb 路径，默认继承全局 kb_dir
  --reuse-gh-auth           优先复用 gh auth token 写入 GH_TOKEN
  --reuse-claude-auth       优先复用本机 Claude 登录态，允许 API Key 留空
  --inject-shell TARGET     显式写入 shell 集成，目前仅支持 bashrc
  --remove-shell TARGET     移除受控 shell 集成块，目前仅支持 bashrc
  --remove-cron             只清理当前用户 crontab 中的 ccclaw 受控规则
  --upgrade-home-repo       升级路径：只刷新现有 home_repo 的受管模板，不执行首装初始化
  -h, --help                显示帮助
USAGE
}

while (($#)); do
  case "$1" in
    --yes) YES=1 ;;
    --simulate) SIMULATE=1 ;;
    --preflight-only) PREFLIGHT_ONLY=1 ;;
    --skip-deps) SKIP_DEPS=1 ;;
    --install-claude) INSTALL_CLAUDE=1 ;;
    --scheduler) shift; SCHEDULER="$1"; SCHEDULER_EXPLICIT=1 ;;
    --task-clone-root) shift; TASK_CLONE_ROOT="$1" ;;
    --app-dir) shift; APP_DIR="$1" ;;
    --home-repo) shift; HOME_REPO="$1" ;;
    --home-repo-mode) shift; HOME_REPO_MODE="$1" ;;
    --home-repo-remote) shift; HOME_REPO_REMOTE="$1"; HOME_REPO_MODE="remote" ;;
    --task-repo-mode) shift; TASK_REPO_MODE="$1" ;;
    --task-repo-remote) shift; TASK_REPO_REMOTE="$1"; TASK_REPO_MODE="remote" ;;
    --task-repo-local) shift; TASK_REPO_LOCAL="$1"; TASK_REPO_MODE="local" ;;
    --task-repo) shift; TASK_REPO="$1" ;;
    --task-repo-path) shift; TASK_REPO_PATH="$1" ;;
    --task-repo-kb-path) shift; TASK_KB_PATH="$1" ;;
    --reuse-gh-auth) REUSE_GH_AUTH=1 ;;
    --reuse-claude-auth) REUSE_CLAUDE_AUTH=1 ;;
    --inject-shell) shift; SHELL_INTEGRATION="$1" ;;
    --remove-shell) shift; REMOVE_SHELL_INTEGRATION="$1" ;;
    --remove-cron) REMOVE_CRON=1 ;;
    --upgrade-home-repo) UPGRADE_HOME_REPO=1 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "未知参数: $1" ;;
  esac
  shift
done

refresh_paths

have() { command -v "$1" >/dev/null 2>&1; }

print_stage() {
  printf '\n== %s ==\n' "$1"
}

prompt_default() {
  local var_name="$1" label="$2" default_value="$3" secret="${4:-0}" response
  if [[ "$YES" -eq 1 ]]; then
    printf -v "$var_name" '%s' "$default_value"
    return 0
  fi
  if [[ "$secret" -eq 1 ]]; then
    read -r -s -p "$label [$default_value]: " response
    printf '\n' >&2
  else
    read -r -p "$label [$default_value]: " response
  fi
  if [[ -z "$response" ]]; then
    response="$default_value"
  fi
  printf -v "$var_name" '%s' "$response"
}

prompt_mode() {
  local var_name="$1" label="$2" default_value="$3" allowed="$4"
  local input default_short
  default_short="$(printf '%s' "${default_value:0:1}" | tr '[:lower:]' '[:upper:]')"
  while true; do
    prompt_default input "$label" "$default_short"
    local normalized
    normalized="$(printf '%s' "$input" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')"
    # 完整单词精确匹配
    case " $allowed " in
      *" $normalized "*) printf -v "$var_name" '%s' "$normalized"; return 0 ;;
    esac
    # 首字母缩写展开
    local match="" word
    for word in $allowed; do
      if [[ "${word:0:1}" == "$normalized" ]]; then
        match="$word"
        break
      fi
    done
    if [[ -n "$match" ]]; then
      printf -v "$var_name" '%s' "$match"
      return 0
    fi
    warn "无效取值: $input；允许值: $allowed"
  done
}

path_needs_sudo() {
  local path="$1"
  [[ "$path" == /opt/* || "$path" == /srv/* || "$path" == /var/* ]]
}

run_maybe_sudo_for_path() {
  local path="$1"
  shift
  if path_needs_sudo "$path" && [[ ! -w "$(dirname "$path")" && ! -w "$path" ]]; then
    sudo "$@"
  else
    "$@"
  fi
}

ensure_path_owner() {
  local path="$1"
  if [[ ! -e "$path" ]]; then
    return 0
  fi
  if path_needs_sudo "$path" && [[ ! -w "$path" ]]; then
    sudo chown -R "$(id -un)":"$(id -gn)" "$path"
  fi
}

ensure_dir() {
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] mkdir -p $*"
    return 0
  fi
  mkdir -p "$@"
}

dir_has_entries() {
  local path="$1"
  [[ -d "$path" ]] || return 1
  find "$path" -mindepth 1 -maxdepth 1 | read -r _
}

first_existing_parent() {
  local path="$1"
  while [[ ! -e "$path" && "$path" != "/" ]]; do
    path="$(dirname "$path")"
  done
  printf '%s\n' "$path"
}

path_writable_or_creatable() {
  local path="$1" existing
  if [[ -e "$path" ]]; then
    [[ -w "$path" ]]
    return $?
  fi
  existing="$(first_existing_parent "$path")"
  [[ -w "$existing" ]]
}

path_is_within() {
  local child="$1" parent="$2"
  child="${child%/}"
  parent="${parent%/}"
  case "$child/" in
    "$parent/"*) return 0 ;;
  esac
  return 1
}

same_existing_path() {
  local source="$1" target="$2"
  [[ -e "$source" && -e "$target" && "$source" -ef "$target" ]]
}

install_release_file() {
  local mode="$1" source="$2" target="$3"
  if same_existing_path "$source" "$target"; then
    log "跳过同路径文件覆盖: $target"
    return 0
  fi
  install -m "$mode" "$source" "$target"
}

copy_release_dir_contents() {
  local source_dir="$1" target_dir="$2"
  if same_existing_path "$source_dir" "$target_dir"; then
    log "跳过同路径目录复制: $target_dir"
    return 0
  fi
  cp -R "$source_dir/." "$target_dir/"
}

append_line_if_missing() {
  local file="$1" line="$2"
  if grep -Fqx "$line" "$file" 2>/dev/null; then
    return 0
  fi
  printf '%s\n' "$line" >> "$file"
}

shell_target_file() {
  case "$1" in
    bashrc) printf '%s\n' "$BASHRC_FILE" ;;
    none|"") printf '\n' ;;
    *) fail "未知 shell 集成目标: $1" ;;
  esac
}

shell_quote_double() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '%s\n' "$value"
}

single_line_text() {
  printf '%s' "$1" | tr '\n' ' ' | sed -E 's/[[:space:]]+/ /g; s/^ //; s/ $//'
}

toml_get_value() {
  local file="$1" section="$2" key="$3"
  [[ -f "$file" ]] || return 0
  awk -v target="[$section]" -v key="$key" '
    BEGIN { in_section=0 }
    /^[[:space:]]*\[/ {
      trimmed=$0
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", trimmed)
      if (trimmed == target) {
        in_section=1
        next
      }
      if (in_section == 1) {
        in_section=0
      }
    }
    in_section == 1 {
      pattern = "^[[:space:]]*" key "[[:space:]]*="
      if ($0 ~ pattern) {
        line=$0
        sub(/^[^=]*=[[:space:]]*/, "", line)
        gsub(/^[\"'\'' ]+|[\"'\'' ]+$/, "", line)
        print line
        exit
      }
    }
  ' "$file"
}

load_existing_scheduler_preferences() {
  local value=""
  [[ -f "$CONFIG_FILE" ]] || return 0
  if [[ "$SCHEDULER_EXPLICIT" -eq 0 ]]; then
    value="$(toml_get_value "$CONFIG_FILE" "scheduler" "mode")"
    if [[ -n "$value" ]]; then
      SCHEDULER="$value"
    fi
  fi
  value="$(toml_get_value "$CONFIG_FILE" "scheduler" "systemd_user_dir")"
  if [[ -n "$value" ]]; then
    SYSTEMD_USER_DIR="$(expand_path "$value")"
  fi
  value="$(toml_get_value "$CONFIG_FILE" "scheduler" "calendar_timezone")"
  if [[ -n "$value" ]]; then
    CALENDAR_TIMEZONE="$value"
  fi
  value="$(toml_get_value "$CONFIG_FILE" "scheduler.timers" "ingest")"
  if [[ -n "$value" ]]; then
    INGEST_CALENDAR="$value"
  fi
  value="$(toml_get_value "$CONFIG_FILE" "scheduler.timers" "run")"
  if [[ -n "$value" ]]; then
    RUN_CALENDAR="$value"
  fi
  value="$(toml_get_value "$CONFIG_FILE" "scheduler.timers" "patrol")"
  if [[ -n "$value" ]]; then
    PATROL_CALENDAR="$value"
  fi
  value="$(toml_get_value "$CONFIG_FILE" "scheduler.timers" "journal")"
  if [[ -n "$value" ]]; then
    JOURNAL_CALENDAR="$value"
  fi
}

calendar_has_explicit_timezone() {
  local calendar="$1" last=""
  set -- $calendar
  [[ "$#" -gt 0 ]] || return 1
  last="${!#}"
  [[ "$last" == "UTC" || "$last" == */* ]]
}

calendar_with_timezone() {
  local calendar="$1"
  if [[ -z "$calendar" || -z "$CALENDAR_TIMEZONE" ]]; then
    printf '%s\n' "$calendar"
    return 0
  fi
  if calendar_has_explicit_timezone "$calendar"; then
    printf '%s\n' "$calendar"
    return 0
  fi
  printf '%s %s\n' "$calendar" "$CALENDAR_TIMEZONE"
}

remove_managed_shell_block() {
  local file="$1" tmp
  [[ -f "$file" ]] || return 0
  tmp="$(mktemp "${TMPDIR:-/tmp}/ccclaw-shell.XXXXXX")"
  awk -v begin="$SHELL_BLOCK_BEGIN" -v end="$SHELL_BLOCK_END" '
    $0 == begin { skip=1; next }
    $0 == end { skip=0; next }
    skip == 0 { print }
  ' "$file" > "$tmp"
  mv "$tmp" "$file"
}

render_shell_block() {
  local shell_dir shell_dir_escaped install_script_escaped
  shell_dir="$(dirname "$BIN_LINK")"
  shell_dir_escaped="$(shell_quote_double "$shell_dir")"
  install_script_escaped="$(shell_quote_double "$APP_DIR/install.sh")"
  cat <<BLOCK
$SHELL_BLOCK_BEGIN
# 由 ccclaw install.sh 管理；如需移除请执行: bash "$install_script_escaped" --remove-shell bashrc
if [ -d "$shell_dir_escaped" ] && [[ ":\$PATH:" != *":$shell_dir_escaped:"* ]]; then
  export PATH="$shell_dir_escaped:\$PATH"
fi
$SHELL_BLOCK_END
BLOCK
}

plan_shell_integration() {
  case "$SHELL_INTEGRATION" in
    none|"")
      SHELL_INTEGRATION_STATUS="disabled"
      SHELL_INTEGRATION_REASON="默认关闭，未写入 shell 配置"
      ;;
    bashrc)
      SHELL_INTEGRATION_STATUS="planned:bashrc"
      SHELL_INTEGRATION_REASON="已请求向 $BASHRC_FILE 写入受控 PATH 块"
      ;;
  esac
}

apply_shell_integration() {
  local target="$1" file
  case "$target" in
    none|"")
      SHELL_INTEGRATION_STATUS="disabled"
      SHELL_INTEGRATION_REASON="默认关闭，未写入 shell 配置"
      return 0
      ;;
    bashrc)
      file="$(shell_target_file "$target")"
      if [[ "$SIMULATE" -eq 1 ]]; then
        log "[simulate] inject shell block into $file"
        SHELL_INTEGRATION_STATUS="planned:bashrc"
        SHELL_INTEGRATION_REASON="模拟写入受控 PATH 块到 $file"
        return 0
      fi
      mkdir -p "$(dirname "$file")"
      touch "$file"
      remove_managed_shell_block "$file"
      if [[ -s "$file" ]]; then
        printf '\n' >> "$file"
      fi
      render_shell_block >> "$file"
      SHELL_INTEGRATION_STATUS="enabled:bashrc"
      SHELL_INTEGRATION_REASON="已向 $file 写入受控 PATH 块"
      ;;
  esac
}

remove_shell_integration() {
  local target="$1" file
  file="$(shell_target_file "$target")"
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] remove shell block from $file"
    return 0
  fi
  remove_managed_shell_block "$file"
  log "已移除受控 shell 集成块: $file"
}

managed_file_has_markers() {
  local file="$1"
  grep -Fq '<!-- ccclaw:managed:start -->' "$file" 2>/dev/null \
    && grep -Fq '<!-- ccclaw:managed:end -->' "$file" 2>/dev/null \
    && grep -Fq '<!-- ccclaw:user:start -->' "$file" 2>/dev/null \
    && grep -Fq '<!-- ccclaw:user:end -->' "$file" 2>/dev/null
}

extract_marked_block() {
  local file="$1" start_marker="$2" end_marker="$3"
  awk -v start_marker="$start_marker" -v end_marker="$end_marker" '
    $0 == start_marker { in_block=1; next }
    $0 == end_marker { in_block=0; exit }
    in_block { print }
  ' "$file"
}

render_template_with_user_block() {
  local template="$1" user_block_file="$2" output="$3"
  awk -v user_start='<!-- ccclaw:user:start -->' \
      -v user_end='<!-- ccclaw:user:end -->' \
      -v user_block_file="$user_block_file" '
    $0 == user_start {
      print
      while ((getline line < user_block_file) > 0) {
        print line
      }
      close(user_block_file)
      in_user=1
      next
    }
    $0 == user_end {
      in_user=0
      print
      next
    }
    in_user { next }
    { print }
  ' "$template" > "$output"
}

has_yaml_frontmatter() {
  local file="$1" first_line
  [[ -f "$file" ]] || return 1
  first_line="$(head -n 1 "$file" 2>/dev/null || true)"
  [[ "$first_line" == "---" ]]
}

is_skill_markdown_path() {
  local path="$1"
  [[ "$path" == */kb/skills/* && "$path" == *.md ]]
}

extract_frontmatter_field() {
  local file="$1" field="$2"
  awk -v field="$field" '
    BEGIN {
      frontmatter=0
      capture=0
      field_pattern="^" field ":[[:space:]]*"
      top_key_pattern="^[A-Za-z0-9_-]+:[[:space:]]*"
    }
    {
      if ($0 == "---") {
        if (frontmatter == 0) {
          frontmatter=1
          next
        }
        if (frontmatter == 1) {
          exit
        }
      }
      if (frontmatter != 1) {
        next
      }
      if (capture) {
        if ($0 ~ top_key_pattern) {
          exit
        }
        print
        next
      }
      if ($0 ~ field_pattern) {
        print
        capture=1
      }
    }
  ' "$file"
}

remove_frontmatter_field() {
  local file="$1" field="$2" tmp_file
  tmp_file="$(mktemp)"
  awk -v field="$field" '
    BEGIN {
      frontmatter=0
      skip=0
      field_pattern="^" field ":[[:space:]]*"
      top_key_pattern="^[A-Za-z0-9_-]+:[[:space:]]*"
    }
    {
      if ($0 == "---") {
        if (frontmatter == 0) {
          frontmatter=1
          print
          next
        }
        if (frontmatter == 1) {
          frontmatter=2
          print
          next
        }
      }
      if (frontmatter != 1) {
        print
        next
      }
      if (skip) {
        if ($0 ~ top_key_pattern) {
          skip=0
        } else {
          next
        }
      }
      if ($0 ~ field_pattern) {
        skip=1
        next
      }
      print
    }
  ' "$file" > "$tmp_file"
  cat "$tmp_file" > "$file"
  rm -f "$tmp_file"
}

append_frontmatter_blocks() {
  local file="$1" blocks_file="$2" tmp_file
  tmp_file="$(mktemp)"
  awk -v blocks_file="$blocks_file" '
    BEGIN { frontmatter=0; inserted=0 }
    /^---$/ {
      if (frontmatter == 0) {
        frontmatter=1
        print
        next
      }
      if (frontmatter == 1) {
        if (!inserted) {
          while ((getline line < blocks_file) > 0) {
            print line
          }
          close(blocks_file)
          inserted=1
        }
        frontmatter=2
        print
        next
      }
    }
    { print }
  ' "$file" > "$tmp_file"
  cat "$tmp_file" > "$file"
  rm -f "$tmp_file"
}

preserve_skill_meta_fields() {
  local old_file="$1" new_file="$2" path_hint="${3:-$new_file}" field blocks_file field_block
  is_skill_markdown_path "$path_hint" || return 0
  has_yaml_frontmatter "$old_file" || return 0
  has_yaml_frontmatter "$new_file" || return 0

  blocks_file="$(mktemp)"
  for field in last_used use_count status gap_signals gap_escalations; do
    field_block="$(mktemp)"
    extract_frontmatter_field "$old_file" "$field" > "$field_block"
    if [[ -s "$field_block" ]]; then
      remove_frontmatter_field "$new_file" "$field"
      cat "$field_block" >> "$blocks_file"
    fi
    rm -f "$field_block"
  done

  if [[ -s "$blocks_file" ]]; then
    append_frontmatter_blocks "$new_file" "$blocks_file"
  fi
  rm -f "$blocks_file"
}

merge_managed_markdown() {
  local template="$1" target="$2" tmp_file user_block_file
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] merge managed markdown $template -> $target"
    return 0
  fi
  mkdir -p "$(dirname "$target")"
  if [[ ! -f "$target" ]]; then
    install -m 644 "$template" "$target"
    return 0
  fi

  tmp_file="$(mktemp)"
  user_block_file="$(mktemp)"
  if managed_file_has_markers "$target"; then
    extract_marked_block "$target" '<!-- ccclaw:user:start -->' '<!-- ccclaw:user:end -->' > "$user_block_file"
  else
    {
      printf '<!-- 从升级前本地版本迁入；以下内容视为用户自定义补充，后续升级保留。 -->\n'
      cat "$target"
    } > "$user_block_file"
  fi

  render_template_with_user_block "$template" "$user_block_file" "$tmp_file"
  preserve_skill_meta_fields "$target" "$tmp_file" "$target"
  if ! cmp -s "$tmp_file" "$target"; then
    cat "$tmp_file" > "$target"
  fi
  rm -f "$tmp_file" "$user_block_file"
}

merge_kb_contracts() {
  local template relative target
  while IFS= read -r template; do
    relative="${template#$DIST_DIR/kb/}"
    target="$HOME_REPO/kb/$relative"
    merge_managed_markdown "$template" "$target"
  done < <(find "$DIST_DIR/kb" -type f -name 'CLAUDE.md' | sort)
}

sync_app_readme() {
  merge_managed_markdown "$DIST_DIR/ops/examples/app-readme.md" "$APP_DIR/README.md"
}

sync_jj_quickref() {
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] install $DIST_DIR/jj.md -> $APP_DIR/jj.md"
    return 0
  fi
  install_release_file 644 "$DIST_DIR/jj.md" "$APP_DIR/jj.md"
}

sync_croncfg_reference() {
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] install $DIST_DIR/croncfg.md -> $CRONCFG_FILE"
    return 0
  fi
  install_release_file 644 "$DIST_DIR/croncfg.md" "$CRONCFG_FILE"
}

normalize_repo_slug() {
  local repo="$1"
  repo="${repo%.git}"
  repo="${repo%/}"
  case "$repo" in
    git@github.com:*) repo="${repo#git@github.com:}" ;;
    ssh://git@github.com/*) repo="${repo#ssh://git@github.com/}" ;;
    https://github.com/*) repo="${repo#https://github.com/}" ;;
    http://github.com/*) repo="${repo#http://github.com/}" ;;
  esac
  if [[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
    printf '%s\n' "$repo"
    return 0
  fi
  return 1
}

clone_url_from_repo_input() {
  local input="$1"
  if slug="$(normalize_repo_slug "$input" 2>/dev/null)"; then
    printf 'https://github.com/%s.git\n' "$slug"
    return 0
  fi
  printf '%s\n' "$input"
}

repo_name_from_slug() {
  local slug="$1"
  printf '%s\n' "${slug##*/}"
}

repo_owner_from_slug() {
  local slug="$1"
  printf '%s\n' "${slug%%/*}"
}

task_clone_path_from_slug() {
  local slug="$1"
  printf '%s/%s/%s\n' "$TASK_CLONE_ROOT" "$(repo_owner_from_slug "$slug")" "$(repo_name_from_slug "$slug")"
}

repo_slug_from_local_repo() {
  local repo_path="$1" remote_url
  remote_url="$(git -C "$repo_path" remote get-url origin 2>/dev/null || true)"
  if [[ -z "$remote_url" ]]; then
    return 1
  fi
  normalize_repo_slug "$remote_url"
}

validate_mode() {
  local label="$1" value="$2" allowed="$3"
  case " $allowed " in
    *" $value "*) return 0 ;;
    *) fail "$label 取值无效: $value；允许值: $allowed" ;;
  esac
}

config_has_targets() {
  grep -q '^\[\[targets\]\]' "$CONFIG_FILE" 2>/dev/null
}

read_env_value() {
  local file="$1" key="$2"
  [[ -f "$file" ]] || return 1
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$file"
}

escape_sed_replacement() {
  printf '%s' "$1" | sed -e 's/[\\/&]/\\&/g'
}

upsert_env_key() {
  local file="$1" key="$2" value="$3" escaped
  escaped="$(escape_sed_replacement "$value")"
  if grep -q "^${key}=" "$file" 2>/dev/null; then
    sed -i "s/^${key}=.*/${key}=${escaped}/" "$file"
  else
    printf '%s=%s\n' "$key" "$value" >> "$file"
  fi
}

probe_claude() {
  local claude_bin claude_ver plugins marketplaces skills_market auth_state install_channel
  claude_bin="$(command -v claude 2>/dev/null || true)"
  claude_ver="$(claude --version 2>/dev/null || true)"
  plugins="$(claude plugin list 2>/dev/null || true)"
  marketplaces="$(claude plugin marketplace list 2>/dev/null || true)"
  skills_market="$(printf '%s' "$marketplaces" | grep -F 'anthropic-agent-skills' || true)"
  auth_state="$(claude auth status --json 2>/dev/null || true)"
  if [[ -n "$auth_state" || -f "$HOME/.claude/.credentials.json" ]]; then
    CLAUDE_AUTH_READY=1
    CLAUDE_AUTH_METHOD="cli-session"
  elif [[ -n "${ANTHROPIC_BASE_URL:-}" && -n "${ANTHROPIC_AUTH_TOKEN:-}" ]]; then
    CLAUDE_AUTH_READY=1
    CLAUDE_AUTH_METHOD="proxy-env"
  else
    CLAUDE_AUTH_READY=0
    CLAUDE_AUTH_METHOD="missing"
  fi
  if claude_install_channel_reachable; then
    install_channel="reachable"
  else
    install_channel="blocked"
  fi
  cat <<INFO
== Claude 探查 ==
- claude_bin: ${claude_bin:-<missing>}
- claude_version: ${claude_ver:-<missing>}
- install_channel: ${install_channel}
- credentials_file: $( [[ -f "$HOME/.claude/.credentials.json" ]] && echo present || echo missing )
- settings_json: $( [[ -f "$HOME/.claude/settings.json" ]] && echo present || echo missing )
- auth_status_json: $( [[ -n "$auth_state" ]] && echo present || echo missing )
- auth_method: ${CLAUDE_AUTH_METHOD}
- installed_plugins: $(printf '%s' "$plugins" | grep -c '@' || true)
- official_marketplace: $(printf '%s' "$marketplaces" | grep -F 'claude-plugins-official' >/dev/null && echo present || echo missing)
- skills_marketplace: $( [[ -n "$skills_market" ]] && echo present || echo missing )
- proxy_env: $( [[ -n "${ANTHROPIC_BASE_URL:-}" && -n "${ANTHROPIC_AUTH_TOKEN:-}" ]] && echo present || echo missing )
- go_version: $(go version 2>/dev/null || echo missing)
- sudo_nopasswd: $(sudo -n true >/dev/null 2>&1 && echo enabled || echo missing)
INFO
}

probe_gh_auth() {
  GH_TOKEN_DETECTED=""
  if ! have gh; then
    GH_AUTH_STATUS="missing_cli"
    return 0
  fi
  if gh auth status >/dev/null 2>&1; then
    GH_AUTH_STATUS="logged_in"
    if [[ "$REUSE_GH_AUTH" -eq 1 ]]; then
      GH_TOKEN_DETECTED="$(gh auth token 2>/dev/null || true)"
    fi
    if [[ -z "$GH_TOKEN_DETECTED" ]]; then
      GH_AUTH_STATUS="logged_in_without_token"
    fi
    return 0
  fi
  GH_AUTH_STATUS="not_logged_in"
}

probe_systemd_user() {
  local dir_owner=""
  SYSTEMD_READY=0
  SYSTEMD_CONTROL_READY=0
  if [[ -d "$SYSTEMD_USER_DIR" ]]; then
    dir_owner="$(stat -c '%U' "$SYSTEMD_USER_DIR" 2>/dev/null || true)"
  fi
  if [[ "$SCHEDULER" == "cron" || "$SCHEDULER" == "none" ]]; then
    SYSTEMD_CHECK_STATUS="skip(${SCHEDULER})"
    SYSTEMD_CONTROL_STATUS="skip(${SCHEDULER})"
    return 0
  fi
  if ! have systemctl; then
    SYSTEMD_CHECK_STATUS="missing_systemctl"
    SYSTEMD_CONTROL_STATUS="missing_systemctl"
    return 0
  fi
  if [[ -d "$SYSTEMD_USER_DIR" && -n "$dir_owner" && "$dir_owner" != "$(id -un)" ]]; then
    SYSTEMD_CHECK_STATUS="dir_owner=${dir_owner}"
    SYSTEMD_CONTROL_STATUS="skip(dir_owner)"
    return 0
  fi
  if ! path_writable_or_creatable "$SYSTEMD_USER_DIR"; then
    SYSTEMD_CHECK_STATUS="dir_not_writable"
    SYSTEMD_CONTROL_STATUS="skip(dir_not_writable)"
    return 0
  fi
  SYSTEMD_READY=1
  SYSTEMD_CHECK_STATUS="ready"
  if systemctl --user show-environment >/dev/null 2>&1; then
    SYSTEMD_CONTROL_READY=1
    SYSTEMD_CONTROL_STATUS="ready"
  else
    SYSTEMD_CONTROL_STATUS="user_bus_unavailable"
  fi
}

probe_crontab() {
  local output="" status=0 text=""
  CRON_READY=0
  if [[ "$SCHEDULER" == "none" ]]; then
    CRON_CHECK_STATUS="skip(none)"
    return 0
  fi
  if ! have crontab; then
    CRON_CHECK_STATUS="missing_crontab"
    return 0
  fi
  output="$(crontab -l 2>&1)" || status=$?
  text="$(single_line_text "$output")"
  if [[ "$status" -eq 0 ]]; then
    CRON_READY=1
    CRON_CHECK_STATUS="ready(existing_entries)"
    return 0
  fi
  if [[ "$text" == *"no crontab for"* || "$text" == *"没有 crontab"* ]]; then
    CRON_READY=1
    CRON_CHECK_STATUS="ready(empty)"
    return 0
  fi
  if [[ "$text" == *"executable file not found"* || "$text" == *"command not found"* ]]; then
    CRON_CHECK_STATUS="missing_crontab"
    return 0
  fi
  CRON_CHECK_STATUS="probe_failed(${text:-unknown})"
}

cron_manual_mode_hint() {
  case "$CRON_CHECK_STATUS" in
    ready*)
      printf '当前检测到可用 crontab；安装器不会自动改写，请按 %s 走专家手工配置。\n' "$CRONCFG_FILE"
      ;;
    missing_crontab)
      printf '当前环境缺少 crontab；本轮只能保持 none，如需 cron 请先安装 cron/cronie，并参考 %s。\n' "$CRONCFG_FILE"
      ;;
    probe_failed*)
      printf '当前 crontab 探查失败(%s)；本轮只能保持 none，请先修复环境或参考 %s。\n' "$CRON_CHECK_STATUS" "$CRONCFG_FILE"
      ;;
    *)
      printf '当前未检测到可用 crontab；本轮按 none 继续，如需专家手工 cron 请参考 %s。\n' "$CRONCFG_FILE"
      ;;
  esac
}

cron_manual_block() {
  cat <<EOF
# >>> ccclaw managed cron >>>
*/4 * * * * $APP_DIR/bin/ccclaw ingest --config $CONFIG_FILE --env-file $ENV_FILE
*/2 * * * * $APP_DIR/bin/ccclaw patrol --config $CONFIG_FILE --env-file $ENV_FILE
50 23 * * * $APP_DIR/bin/ccclaw journal --config $CONFIG_FILE --env-file $ENV_FILE
# <<< ccclaw managed cron <<<
EOF
}

cron_manual_summary() {
  cat <<EOF
详细专家说明：$CRONCFG_FILE
可先复核：
crontab -l
$APP_DIR/bin/ccclaw scheduler status --config $CONFIG_FILE --env-file $ENV_FILE
如需专家手工托管，可选两种方式：
1. 使用 CLI 专家工具：
   $APP_DIR/bin/ccclaw --config $CONFIG_FILE --env-file $ENV_FILE scheduler enable-cron
2. 手工写入以下 crontab：
$(cron_manual_block)
EOF
}

decide_scheduler() {
  case "$SCHEDULER" in
    systemd)
      if [[ "$SYSTEMD_READY" -eq 1 ]]; then
        SCHEDULER_EFFECTIVE="systemd"
        if [[ "$SYSTEMD_CONTROL_READY" -eq 1 ]]; then
          SCHEDULER_REASON="user systemd 可部署，且当前会话可直接控制"
        else
          SCHEDULER_REASON="user systemd 单元可部署，但当前会话未直连 user bus，安装后需手工启用 timer"
        fi
      else
        fail "已显式指定 --scheduler systemd，但当前环境不可用: $SYSTEMD_CHECK_STATUS"
      fi
      ;;
    auto)
      if [[ "$SYSTEMD_READY" -eq 1 ]]; then
        SCHEDULER_EFFECTIVE="systemd"
        if [[ "$SYSTEMD_CONTROL_READY" -eq 1 ]]; then
          SCHEDULER_REASON="自动选择 user systemd"
        else
          SCHEDULER_REASON="自动选择 user systemd；当前会话未直连 user bus，安装后需手工启用 timer"
        fi
      else
        probe_crontab
        SCHEDULER_EFFECTIVE="none"
        SCHEDULER_REASON="user systemd 不可用，安装器不再自动托管 cron；本次按 none 继续。$(cron_manual_mode_hint | tr '\n' ' ')(systemd=$SYSTEMD_CHECK_STATUS cron=$CRON_CHECK_STATUS)"
      fi
      ;;
    cron)
      probe_crontab
      fail "安装器不再自动托管 cron。请改用 --scheduler auto|systemd|none，并参考 $CRONCFG_FILE 进行专家手工配置 (systemd=$SYSTEMD_CHECK_STATUS cron=$CRON_CHECK_STATUS)"
      ;;
    none)
      SCHEDULER_EFFECTIVE="none"
      SCHEDULER_REASON="按显式参数执行"
      ;;
  esac
}

print_preflight() {
  local gh_line systemd_line cron_line clone_root_line
  probe_gh_auth
  probe_systemd_user
  decide_scheduler
  case "$GH_AUTH_STATUS" in
    logged_in) gh_line="OK: 已登录 gh，支持复用 GH_TOKEN" ;;
    logged_in_without_token) gh_line="WARN: gh 已登录，但未能直接提取 token；后续可能仍需手工补录 GH_TOKEN" ;;
    not_logged_in) gh_line="WARN: 未检测到 gh 登录态；后续需要手工填写 GH_TOKEN" ;;
    missing_cli) gh_line="WARN: 未找到 gh；后续需要手工填写 GH_TOKEN" ;;
    *) gh_line="WARN: gh 状态未知" ;;
  esac
  case "$SYSTEMD_CHECK_STATUS" in
    ready)
      if [[ "$SYSTEMD_CONTROL_READY" -eq 1 ]]; then
        systemd_line="OK: user systemd 可部署，当前会话可直接控制"
      else
        systemd_line="WARN: user systemd 单元可部署，但当前会话未直连 user bus；安装后需在登录会话中手工启用 timer"
      fi
      ;;
    skip*) systemd_line="OK: 已跳过 user systemd 探查，当前调度模式 $SCHEDULER" ;;
    *) systemd_line="WARN: user systemd 不可直接使用($SYSTEMD_CHECK_STATUS)，将按 $SCHEDULER_EFFECTIVE 继续" ;;
  esac
  case "$CRON_CHECK_STATUS" in
    ready*) cron_line="INFO: 已检测到 crontab；仅供专家手工配置，安装器不会自动改写" ;;
    skip*) cron_line="OK: 已跳过 crontab 探查，当前调度模式 $SCHEDULER" ;;
    missing_crontab) cron_line="WARN: 未找到 crontab；当前无法走专家手工 cron" ;;
    probe_failed*) cron_line="WARN: crontab 探查失败($CRON_CHECK_STATUS)，当前无法走专家手工 cron" ;;
    *) cron_line="INFO: crontab 尚未参与本轮调度决策" ;;
  esac
  if [[ "$TASK_REPO_MODE" == "remote" ]]; then
    clone_root_line="OK: remote 任务仓库 clone 根目录固定为 $TASK_CLONE_ROOT"
  else
    clone_root_line="OK: 当前未启用 remote 任务仓库绑定"
  fi
  print_stage "安装前体检"
  cat <<INFO
- Claude: $( [[ "$CLAUDE_AUTH_READY" -eq 1 ]] && printf 'OK: 已检测到 %s' "$CLAUDE_AUTH_METHOD" || printf 'WARN: 未检测到可复用登录态，后续可能需要手工补录 API/代理配置' )
- GitHub: $gh_line
- Systemd: $systemd_line
- Cron: $cron_line
- Remote Clone Root: $clone_root_line
- 计划调度模式: 请求=$SCHEDULER, 生效=$SCHEDULER_EFFECTIVE
INFO
}

print_flow() {
  local home_repo_mode_summary="$HOME_REPO_MODE"
  local home_repo_step_title="5. 初始化或接管本体仓库："
  local home_repo_step_body="   - init: 在目标目录 git init，并写入 dist/kb 初始记忆树
   - remote: clone 指定仓库到目标目录，再补齐 kb 初始树
   - local: 接管本地已有 git 仓库，再补齐 kb 初始树"
  if [[ "$UPGRADE_HOME_REPO" -eq 1 ]]; then
    home_repo_mode_summary="upgrade-refresh"
    home_repo_step_title="5. 刷新现有本体仓库受管模板："
    home_repo_step_body="   - 仅刷新 kb/**/CLAUDE.md 受管模板
   - 不再执行首次初始化专属逻辑
   - 不自动创建本体 README.md / CLAUDE.md / .gitignore"
  fi
  cat <<FLOW
== 计划执行流程 ==
1. 探查现有环境：claude / gh / rg / tmux / rtk / git / node / npm / uv / systemd --user
2. 决定程序目录与本体仓库目录：
   - 程序目录: $APP_DIR
   - 本体仓库: $HOME_REPO
   - 本体仓库模式: $home_repo_mode_summary
   - 调度模式: $SCHEDULER
3. 生成固定隐私配置：
   - .env: $ENV_FILE
   - 优先复用已有 .env 与 gh auth token
4. 生成普通配置：
   - config.toml: $CONFIG_FILE
   - 固定写入官方控制仓库: $CONTROL_REPO
   - 记录 path/执行器/调度等非敏感配置
$home_repo_step_title
$home_repo_step_body
6. Claude 适配：
   - 先探查官方安装通道可达性
   - 交互模式可确认后执行官方安装脚本
   - 非交互模式仅在 --install-claude 时自动安装
   - 若本机已有 claude，只读探查并复用现有登录态/代理配置
   - 不自动修改 marketplace / plugins / rtk 全局状态
7. 安装程序文件：
   - $APP_DIR/bin/ccclaw
   - $APP_DIR/bin/ccclaude
   - $APP_DIR/install.sh
   - $APP_DIR/upgrade.sh
   - $APP_DIR/ops/*
   - shell 集成默认关闭；仅在 --inject-shell bashrc 时写入受控 PATH 块
8. 安装基础工具：
   - 必装: git gh rg tmux curl wget golang
   - 能力工具: node npm uv
   - token 优化: rtk
9. Claude 生态处理：
   - 只读探查当前 CLI / 登录态 / marketplace / plugins
   - 不默认追加 marketplace、plugins、example-skills
   - 不默认执行 rtk init --global
   - 执行器默认走: $CLAUDE_WRAPPER
10. 安装调度后端：
   - 请求模式为 auto|systemd 时先体检 user systemd
   - user systemd 可用时写入 $SYSTEMD_USER_DIR
   - user systemd 不可用时，安装器进入 none + 手工 cron 指引
   - 安装器不再自动写入当前用户 crontab；cron 仅保留为专家手工工具
11. 任务仓库绑定：
   - none: 本轮不绑定
   - remote: clone 到 $TASK_CLONE_ROOT 下约定入口，并写入 config.toml
   - local: 接管已有本地仓库，并写入 config.toml
12. 升级策略：
   - upgrade.sh 只升级程序发布树
   - kb/**/CLAUDE.md 采用受管区块刷新，保留用户自定义区块
   - 不自动覆盖本体仓库记忆内容

== 交互项矩阵 ==
- 必填且敏感(.env): GH_TOKEN
- 可选且敏感(.env): ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN, GREPTILE_API_KEY
- 必填但可默认(config.toml): app_dir, home_repo, kb_dir, var_dir, log_dir
- 本体仓库模式: init|remote|local
- 任务仓库模式: none|remote|local
- 调度器模式: auto|systemd|cron|none
- 可自动探查并默认继承: claude 路径、登录态、plugin marketplace、已装插件
FLOW
}

collect_inputs() {
  print_stage "阶段 1/4 安装拓扑"
  prompt_default APP_DIR "程序目录" "$APP_DIR"
  prompt_mode HOME_REPO_MODE "本体仓库模式 (I)nit/(R)emote/(L)ocal" "$HOME_REPO_MODE" "init remote local"
  prompt_default HOME_REPO "本体仓库目录" "$HOME_REPO"
  case "$HOME_REPO_MODE" in
    remote)
      prompt_default HOME_REPO_REMOTE "本体远程仓库(owner/repo 或 URL)" "$HOME_REPO_REMOTE"
      ;;
    local)
      prompt_default HOME_REPO "本地本体仓库路径" "$HOME_REPO"
      ;;
  esac

  print_stage "阶段 2/4 任务仓库绑定"
  prompt_mode TASK_REPO_MODE "任务仓库模式 (N)one/(R)emote/(L)ocal" "$TASK_REPO_MODE" "none remote local"
  case "$TASK_REPO_MODE" in
    remote)
      prompt_default TASK_REPO_REMOTE "任务远程仓库(owner/repo 或 URL)" "$TASK_REPO_REMOTE"
      if [[ -z "$TASK_REPO" ]]; then
        TASK_REPO="$(normalize_repo_slug "$TASK_REPO_REMOTE" 2>/dev/null || true)"
      fi
      if [[ -z "$TASK_REPO_PATH" && -n "$TASK_REPO" ]]; then
        TASK_REPO_PATH="$(task_clone_path_from_slug "$TASK_REPO")"
      fi
      prompt_default TASK_REPO "任务仓库 owner/repo" "$TASK_REPO"
      prompt_default TASK_REPO_PATH "任务仓库本地路径(固定入口位于 /opt/src/3claw)" "$TASK_REPO_PATH"
      prompt_default TASK_KB_PATH "任务仓库 kb 路径(可留空继承全局)" "$TASK_KB_PATH"
      ;;
    local)
      while true; do
        prompt_default TASK_REPO_LOCAL "本地任务仓库绝对路径" "$TASK_REPO_LOCAL"
        if [[ -z "$TASK_REPO_LOCAL" ]]; then
          warn "路径不可为空，请输入本地已 clone 的任务仓库绝对路径"
          continue
        fi
        if [[ "$TASK_REPO_LOCAL" != /* ]]; then
          warn "必须为绝对路径: $TASK_REPO_LOCAL"
          continue
        fi
        if [[ ! -d "$TASK_REPO_LOCAL" ]]; then
          warn "目录不存在: $TASK_REPO_LOCAL"
          continue
        fi
        if [[ ! -d "$TASK_REPO_LOCAL/.git" ]]; then
          fail "不是 git 仓库（无 .git 目录）: $TASK_REPO_LOCAL"
        fi
        break
      done
      if [[ -z "$TASK_REPO" ]]; then
        TASK_REPO="$(repo_slug_from_local_repo "$TASK_REPO_LOCAL" 2>/dev/null || true)"
      fi
      if [[ -n "$TASK_REPO" ]]; then
        log "已从本地仓库 origin 推断任务仓库: $TASK_REPO"
      else
        warn "未从 origin 推断出 owner/repo；运行时路由仍依赖该值，请手工输入。"
        prompt_default TASK_REPO "任务仓库 owner/repo" "$TASK_REPO"
      fi
      prompt_default TASK_KB_PATH "任务仓库 kb 路径(可留空继承全局)" "$TASK_KB_PATH"
      ;;
  esac

  print_stage "阶段 3/4 调度器"
  if [[ "$SCHEDULER_EXPLICIT" -eq 0 && "$YES" -eq 0 ]]; then
    local scheduler_prompt_default="$SCHEDULER"
    if [[ "$scheduler_prompt_default" == "cron" ]]; then
      scheduler_prompt_default="auto"
    fi
    prompt_mode SCHEDULER "调度器模式 (A)uto/(S)ystemd/(N)one" "$scheduler_prompt_default" "auto systemd none"
  fi
  refresh_paths
}

validate_inputs() {
  validate_shell_options
  validate_mode "本体仓库模式" "$HOME_REPO_MODE" "init remote local"
  validate_mode "任务仓库模式" "$TASK_REPO_MODE" "none remote local"
  validate_mode "调度器模式" "$SCHEDULER" "auto systemd cron none"
  case "$HOME_REPO_MODE" in
    remote)
      [[ -n "$HOME_REPO_REMOTE" ]] || fail "remote 模式下必须提供 --home-repo-remote"
      ;;
    local)
      [[ -n "$HOME_REPO" ]] || fail "local 模式下必须提供本地本体仓库路径"
      ;;
  esac
  case "$TASK_REPO_MODE" in
    remote)
      [[ -n "$TASK_REPO_REMOTE" ]] || fail "remote 模式下必须提供 --task-repo-remote"
      if ! normalize_repo_slug "$TASK_REPO" >/dev/null 2>&1; then
        fail "无法识别任务仓库 owner/repo: $TASK_REPO"
      fi
      [[ -n "$TASK_REPO_PATH" ]] || fail "remote 模式下必须提供任务仓库本地路径"
      if ! path_is_within "$TASK_REPO_PATH" "$TASK_CLONE_ROOT"; then
        fail "remote 模式下任务仓库本地路径必须位于固定 clone 入口 $TASK_CLONE_ROOT"
      fi
      ;;
    local)
      [[ -n "$TASK_REPO_LOCAL" ]] || fail "local 模式下必须提供 --task-repo-local"
      if ! normalize_repo_slug "$TASK_REPO" >/dev/null 2>&1; then
        fail "local 模式下无法从仓库自动推断 owner/repo，且未提供有效 --task-repo"
      fi
      ;;
  esac
}

validate_shell_options() {
  validate_mode "shell 集成模式" "$SHELL_INTEGRATION" "none bashrc"
  validate_mode "shell 回滚模式" "$REMOVE_SHELL_INTEGRATION" "none bashrc"
  if [[ "$SHELL_INTEGRATION" != "none" && "$REMOVE_SHELL_INTEGRATION" != "none" ]]; then
    fail "--inject-shell 与 --remove-shell 不能同时使用"
  fi
}

ensure_system_packages() {
  local missing_tools=()
  local missing_packages=()
  local required=(git gh rg tmux curl wget)
  local optional=(node npm uv)
  for tool in "${required[@]}"; do
    if ! have "$tool"; then
      missing_tools+=("$tool")
      missing_packages+=("$tool")
    fi
  done
  if ! have go; then
    missing_tools+=("go")
    missing_packages+=("golang-go")
  fi
  if [[ "${#missing_tools[@]}" -eq 0 ]]; then
    log "基础工具已齐全"
  elif [[ "$SKIP_DEPS" -eq 1 ]]; then
    warn "跳过依赖安装，当前缺失: ${missing_tools[*]}"
  elif have apt-get; then
    log "尝试用 apt-get 安装缺失工具: ${missing_tools[*]}"
    if [[ "$SIMULATE" -eq 1 ]]; then
      log "[simulate] sudo apt-get update && sudo apt-get install -y ${missing_packages[*]}"
    else
      sudo apt-get update
      sudo apt-get install -y "${missing_packages[@]}"
    fi
  else
    warn "未找到可用包管理器，请手工安装: ${missing_tools[*]}"
  fi
  for tool in "${optional[@]}"; do
    if have "$tool"; then
      log "已发现可选工具: $tool"
    fi
  done
  if have jj; then
    log "已发现 jj: $(jj --version 2>/dev/null || echo unknown)"
    return 0
  fi
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] install jj prebuilt binary into /usr/local/bin/jj"
    return 0
  fi
  local arch archive_url tmp_dir archive_file binary_path
  case "$(uname -m)" in
    x86_64|amd64) arch="x86_64" ;;
    aarch64|arm64) arch="aarch64" ;;
    *) fail "当前架构暂不支持自动安装 jj: $(uname -m)" ;;
  esac
  archive_url="https://github.com/jj-vcs/jj/releases/latest/download/jj-${arch}-unknown-linux-musl.tar.gz"
  tmp_dir="$(mktemp -d)"
  archive_file="$tmp_dir/jj.tar.gz"
  if have curl; then
    curl -fsSL "$archive_url" -o "$archive_file"
  elif have wget; then
    wget -qO "$archive_file" "$archive_url"
  else
    fail "缺少 curl/wget，无法自动安装 jj"
  fi
  tar -xzf "$archive_file" -C "$tmp_dir"
  binary_path="$(find "$tmp_dir" -type f -name jj | head -n 1)"
  [[ -n "$binary_path" ]] || fail "jj 安装包解压后未找到可执行文件"
  sudo install -m 755 "$binary_path" /usr/local/bin/jj
  rm -rf "$tmp_dir"
  have jj || fail "jj 安装后仍不可用"
  log "已安装 jj: $(jj --version 2>/dev/null || echo unknown)"
}

claude_install_channel_reachable() {
  if have curl; then
    curl -fsSIL --connect-timeout 5 --max-time 15 https://claude.ai/install.sh >/dev/null 2>&1
    return $?
  fi
  if have wget; then
    wget -q --spider --timeout=15 https://claude.ai/install.sh
    return $?
  fi
  return 1
}

install_claude_official() {
  if have claude; then
    log "已发现 claude: $(command -v claude)"
    return 0
  fi
  if ! claude_install_channel_reachable; then
    fail "Claude 官方安装通道不可达，请先解决网络问题后重试；当前推荐命令: curl -fsSL https://claude.ai/install.sh | bash"
  fi
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] curl -fsSL https://claude.ai/install.sh | bash"
    return 0
  fi
  if [[ "$YES" -eq 1 && "$INSTALL_CLAUDE" -ne 1 ]]; then
    warn "非交互模式下未显式传入 --install-claude，跳过 Claude 自动安装"
    return 0
  fi
  if [[ "$YES" -ne 1 ]]; then
    local answer
    read -r -p "检测到未安装 Claude，是否执行官方安装脚本? [y/N]: " answer
    if [[ ! "$answer" =~ ^[Yy]$ ]]; then
      warn "已跳过 Claude 安装；后续请手工执行: curl -fsSL https://claude.ai/install.sh | bash"
      return 0
    fi
  fi
  log "执行 Claude 官方安装脚本"
  curl -fsSL https://claude.ai/install.sh | bash
}

install_rtk() {
  if have rtk; then
    log "rtk 已存在: $(command -v rtk)"
    return 0
  fi
  if [[ "$SKIP_DEPS" -eq 1 ]]; then
    warn "跳过 rtk 安装；请后续手工执行官方安装脚本"
    return 0
  fi
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh"
    return 0
  fi
  log "按官方 quick install 安装 rtk"
  curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh
}

create_claude_wrapper() {
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] install $DIST_DIR/ops/scripts/ccclaude -> $CLAUDE_WRAPPER"
    return 0
  fi
  install_release_file 755 "$DIST_DIR/ops/scripts/ccclaude" "$CLAUDE_WRAPPER"
}

ensure_env_key() {
  local key="$1"
  if grep -q "^${key}=" "$ENV_FILE" 2>/dev/null; then
    return 0
  fi
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] append ${key}= to $ENV_FILE"
    return 0
  fi
  printf '%s=\n' "$key" >> "$ENV_FILE"
}

create_env_file() {
  local gh_token="${GH_TOKEN:-}"
  local anthropic_key="${ANTHROPIC_API_KEY:-}"
  local anthropic_base_url="${ANTHROPIC_BASE_URL:-}"
  local anthropic_auth_token="${ANTHROPIC_AUTH_TOKEN:-}"
  local greptile_key="${GREPTILE_API_KEY:-}"
  local previous_umask
  if [[ -z "$gh_token" && -n "$GH_TOKEN_DETECTED" ]]; then
    gh_token="$GH_TOKEN_DETECTED"
    GH_TOKEN_SOURCE="gh auth token"
  fi
  if [[ -f "$ENV_FILE" ]]; then
    log "复用已有隐私配置: $ENV_FILE"
    ensure_env_key "GH_TOKEN"
    ensure_env_key "ANTHROPIC_API_KEY"
    ensure_env_key "ANTHROPIC_BASE_URL"
    ensure_env_key "ANTHROPIC_AUTH_TOKEN"
    ensure_env_key "GREPTILE_API_KEY"
    if [[ -n "$gh_token" && -z "$(read_env_value "$ENV_FILE" "GH_TOKEN")" ]]; then
      if [[ "$SIMULATE" -eq 1 ]]; then
        log "[simulate] 回填 GH_TOKEN 到 $ENV_FILE"
      else
        upsert_env_key "$ENV_FILE" "GH_TOKEN" "$gh_token"
      fi
    fi
    if [[ -n "$(read_env_value "$ENV_FILE" "GH_TOKEN")" ]]; then
      GH_TOKEN_SOURCE="已有 .env"
    elif [[ -n "$gh_token" ]]; then
      GH_TOKEN_SOURCE="gh auth token"
    else
      GH_TOKEN_SOURCE="留空"
    fi
    [[ -n "$(read_env_value "$ENV_FILE" "ANTHROPIC_API_KEY")" ]] && ANTHROPIC_API_KEY_SOURCE="已有 .env" || ANTHROPIC_API_KEY_SOURCE="留空"
    [[ -n "$(read_env_value "$ENV_FILE" "ANTHROPIC_BASE_URL")" ]] && ANTHROPIC_BASE_URL_SOURCE="已有 .env" || ANTHROPIC_BASE_URL_SOURCE="留空"
    [[ -n "$(read_env_value "$ENV_FILE" "ANTHROPIC_AUTH_TOKEN")" ]] && ANTHROPIC_AUTH_TOKEN_SOURCE="已有 .env" || ANTHROPIC_AUTH_TOKEN_SOURCE="留空"
    [[ -n "$(read_env_value "$ENV_FILE" "GREPTILE_API_KEY")" ]] && GREPTILE_API_KEY_SOURCE="已有 .env" || GREPTILE_API_KEY_SOURCE="留空"
    return 0
  fi
  print_stage "阶段 4/4 凭据复用与补录"
  if [[ -z "$gh_token" ]]; then
    prompt_default gh_token "GitHub Token (issues read/write)" "$gh_token" 1
    [[ -n "$gh_token" ]] && GH_TOKEN_SOURCE="本次输入" || GH_TOKEN_SOURCE="留空"
  fi
  if [[ "$GH_TOKEN_SOURCE" == "未写入" ]]; then
    [[ -n "$gh_token" ]] && GH_TOKEN_SOURCE="gh auth token" || GH_TOKEN_SOURCE="留空"
  fi
  if [[ "$REUSE_CLAUDE_AUTH" -eq 1 && "$CLAUDE_AUTH_READY" -eq 1 ]]; then
    log "已检测到 Claude 登录态($CLAUDE_AUTH_METHOD)，允许直接复用 CLI 会话；ANTHROPIC_API_KEY 可留空。"
  fi
  prompt_default anthropic_key "Anthropic API Key(可留空，若本机 claude 凭据已可用)" "$anthropic_key" 1
  prompt_default anthropic_base_url "Anthropic Base URL(代理模式可填，默认留空)" "$anthropic_base_url" 1
  prompt_default anthropic_auth_token "Anthropic Auth Token(代理模式可填，默认留空)" "$anthropic_auth_token" 1
  prompt_default greptile_key "Greptile API Key(可留空)" "$greptile_key" 1
  [[ -n "$anthropic_key" ]] && ANTHROPIC_API_KEY_SOURCE="本次输入" || ANTHROPIC_API_KEY_SOURCE="留空"
  [[ -n "$anthropic_base_url" ]] && ANTHROPIC_BASE_URL_SOURCE="本次输入" || ANTHROPIC_BASE_URL_SOURCE="留空"
  [[ -n "$anthropic_auth_token" ]] && ANTHROPIC_AUTH_TOKEN_SOURCE="本次输入" || ANTHROPIC_AUTH_TOKEN_SOURCE="留空"
  [[ -n "$greptile_key" ]] && GREPTILE_API_KEY_SOURCE="本次输入" || GREPTILE_API_KEY_SOURCE="留空"
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] write $ENV_FILE"
    return 0
  fi
  previous_umask="$(umask)"
  umask 177
  cat > "$ENV_FILE" <<ENV
# 所有会导致经济损失的隐私信息都在这里。
GH_TOKEN=${gh_token}
ANTHROPIC_API_KEY=${anthropic_key}
ANTHROPIC_BASE_URL=${anthropic_base_url}
ANTHROPIC_AUTH_TOKEN=${anthropic_auth_token}
GREPTILE_API_KEY=${greptile_key}
ENV
  umask "$previous_umask"
  chmod 600 "$ENV_FILE"
}

create_config_file() {
  if [[ -f "$CONFIG_FILE" ]]; then
    CONFIG_FILE_ALREADY_EXISTS=1
    log "保留已有普通配置: $CONFIG_FILE"
    if [[ -x "$APP_DIR/bin/ccclaw" ]]; then
      local migrate_out
      migrate_out="$("$APP_DIR/bin/ccclaw" --config "$CONFIG_FILE" config migrate 2>&1)" \
        || fail "自动迁移现有配置失败: $migrate_out"
      log "$migrate_out"
    fi
    return 0
  fi
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] write $CONFIG_FILE"
    return 0
  fi
  cat > "$CONFIG_FILE" <<CFG
# default_target:
# - 留空时，运行时必须依赖 Issue body 中的 target_repo: owner/repo
# - 仅在存在多个 [[targets]] 时建议显式指定默认值
default_target = ""

# GitHub 控制面配置。
# control_repo 固定指向官方控制仓库，不接受安装期自定义。
[github]
control_repo = "$CONTROL_REPO"
issue_label = "ccclaw"
# ingest 每轮只观察控制仓库与已启用 target 仓库中的 open issues。
# - 只统计匹配 issue_label 的 Issue
# - 不是并发数，也不是 run 阶段的执行上限
limit = 20

# 固定路径边界：
# - app_dir: 程序树
# - home_repo: 本体记忆仓库
# - kb_dir: 默认知识库目录
# - env_file: 所有敏感信息唯一入口
[paths]
app_dir = "$APP_DIR"
home_repo = "$HOME_REPO"
var_dir = "$VAR_DIR"
log_dir = "$LOG_DIR"
kb_dir = "$KB_DIR"
env_file = "$ENV_FILE"

# 执行器默认走 ccclaude 包装器；当前包装器默认直连 claude，不再拼接 rtk proxy。
[executor]
provider = "claude-code"
command = ["$CLAUDE_WRAPPER"]
timeout = "30m"
mode = "daemon"

# 调度策略配置：
# - mode: 安装时请求的调度模式 auto|systemd|cron|none
# - systemd_user_dir: user systemd 单元写入目录
# - calendar_timezone: systemd timer 周期解释时区；默认按北京时间 Asia/Shanghai
[scheduler]
mode = "$SCHEDULER"
systemd_user_dir = "$SYSTEMD_USER_DIR"
calendar_timezone = "$CALENDAR_TIMEZONE"

# systemd timer 周期，采用 systemd OnCalendar 语法。
# - "*-*-* 23:50:00" 表示“每年-每月-每日 23:50:00”
# - 若要配置凌晨 01:01:42，可写为 "*-*-* 01:01:42"
# - 若表达式未显式带时区，运行时会自动追加 scheduler.calendar_timezone
[scheduler.timers]
ingest = "$INGEST_CALENDAR"
patrol = "$PATROL_CALENDAR"
journal = "$JOURNAL_CALENDAR"

# 调度日志视图：
# - level: `ccclaw scheduler logs` 默认 journal 优先级过滤
# - 允许值: emerg|alert|crit|err|warning|notice|info|debug
# - archive_dir: `ccclaw scheduler logs --archive` 默认归档目录
# - retention_days: 只清理 ccclaw 受管归档；超过天数的旧文件会删除
# - max_files: 只统计 ccclaw 受管归档；超过上限时删除最旧文件
# - compress: 新归档保留明文，历史 `.log` 自动压缩为 `.log.gz`
[scheduler.logs]
level = "info"
archive_dir = "$LOG_DIR/scheduler"
retention_days = 30
max_files = 200
compress = true

# maintain 及以上权限的 Issue 自动执行；其他情况需要受信任成员评论 /ccclaw + 同义词。
[approval]
minimum_permission = "maintain"
words = ["approve", "go", "confirm", "批准", "agree", "同意", "推进", "通过", "ok"]
reject_words = ["reject", "no", "cancel", "nil", "null", "拒绝", "000"]

# 任务仓库样例：
# [[targets]]
# repo = "owner/repo"
# local_path = "/opt/src/3claw/owner/repo"
# kb_path = "$KB_DIR"
# executor_mode = "tmux" # 可选: tmux|daemon，留空继承 [executor].mode
# disabled = false
CFG
}

sync_scheduler_config() {
  if [[ ! -x "$APP_DIR/bin/ccclaw" || ! -f "$CONFIG_FILE" ]]; then
    return 0
  fi
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] $APP_DIR/bin/ccclaw --config $CONFIG_FILE config set-scheduler --mode $SCHEDULER --systemd-user-dir $SYSTEMD_USER_DIR --calendar-timezone $CALENDAR_TIMEZONE --ingest-calendar '$INGEST_CALENDAR' --patrol-calendar '$PATROL_CALENDAR' --journal-calendar '$JOURNAL_CALENDAR'"
    return 0
  fi
  "$APP_DIR/bin/ccclaw" --config "$CONFIG_FILE" config set-scheduler \
    --mode "$SCHEDULER" \
    --systemd-user-dir "$SYSTEMD_USER_DIR" \
    --calendar-timezone "$CALENDAR_TIMEZONE" \
    --ingest-calendar "$INGEST_CALENDAR" \
    --patrol-calendar "$PATROL_CALENDAR" \
    --journal-calendar "$JOURNAL_CALENDAR" >/dev/null
}

create_user_systemd_units() {
  local ingest_service="$SYSTEMD_USER_DIR/ccclaw-ingest.service"
  local ingest_timer="$SYSTEMD_USER_DIR/ccclaw-ingest.timer"
  local patrol_service="$SYSTEMD_USER_DIR/ccclaw-patrol.service"
  local patrol_timer="$SYSTEMD_USER_DIR/ccclaw-patrol.timer"
  local journal_service="$SYSTEMD_USER_DIR/ccclaw-journal.service"
  local journal_timer="$SYSTEMD_USER_DIR/ccclaw-journal.timer"
  local archive_service="$SYSTEMD_USER_DIR/ccclaw-archive.service"
  local archive_timer="$SYSTEMD_USER_DIR/ccclaw-archive.timer"
  local ingest_calendar patrol_calendar journal_calendar archive_calendar
  if [[ "$SCHEDULER_EFFECTIVE" != "systemd" ]]; then
    log "跳过 user systemd 单元部署；当前调度模式: $SCHEDULER_EFFECTIVE"
    return 0
  fi
  ensure_dir "$SYSTEMD_USER_DIR"
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] write user systemd units into $SYSTEMD_USER_DIR"
    return 0
  fi
  ingest_calendar="$(calendar_with_timezone "$INGEST_CALENDAR")"
  patrol_calendar="$(calendar_with_timezone "$PATROL_CALENDAR")"
  journal_calendar="$(calendar_with_timezone "$JOURNAL_CALENDAR")"
  archive_calendar="$(calendar_with_timezone "Mon 02:00:00")"
  cat > "$ingest_service" <<UNIT
[Unit]
Description=ccclaw ingest service
After=network-online.target

[Service]
Type=oneshot
WorkingDirectory=$APP_DIR
ExecStart=$APP_DIR/bin/ccclaw ingest --config $CONFIG_FILE --env-file $ENV_FILE
UNIT
  cat > "$ingest_timer" <<UNIT
[Unit]
Description=Run ccclaw ingest on schedule

[Timer]
OnCalendar=$ingest_calendar
Persistent=true
Unit=ccclaw-ingest.service

[Install]
WantedBy=timers.target
UNIT
  cat > "$patrol_service" <<UNIT
[Unit]
Description=ccclaw patrol service
After=network-online.target

[Service]
Type=oneshot
WorkingDirectory=$APP_DIR
ExecStart=$APP_DIR/bin/ccclaw patrol --config $CONFIG_FILE --env-file $ENV_FILE
UNIT
  cat > "$patrol_timer" <<UNIT
[Unit]
Description=Run ccclaw patrol on schedule

[Timer]
OnCalendar=$patrol_calendar
Persistent=true
Unit=ccclaw-patrol.service

[Install]
WantedBy=timers.target
UNIT
  cat > "$journal_service" <<UNIT
[Unit]
Description=ccclaw journal service
After=network-online.target

[Service]
Type=oneshot
WorkingDirectory=$APP_DIR
ExecStart=$APP_DIR/bin/ccclaw journal --config $CONFIG_FILE --env-file $ENV_FILE
UNIT
  cat > "$journal_timer" <<UNIT
[Unit]
Description=Run ccclaw journal on schedule

[Timer]
OnCalendar=$journal_calendar
Persistent=true
Unit=ccclaw-journal.service

[Install]
WantedBy=timers.target
UNIT
  cat > "$archive_service" <<UNIT
[Unit]
Description=ccclaw archive service
After=network-online.target

[Service]
Type=oneshot
WorkingDirectory=$APP_DIR
ExecStart=$APP_DIR/bin/ccclaw archive --config $CONFIG_FILE --env-file $ENV_FILE
UNIT
  cat > "$archive_timer" <<UNIT
[Unit]
Description=Run ccclaw weekly archive on schedule

[Timer]
OnCalendar=$archive_calendar
Persistent=true
Unit=ccclaw-archive.service

[Install]
WantedBy=timers.target
UNIT
}

append_legacy_cleanup_note() {
  local reason="$1"
  case "$LEGACY_RUN_CLEANUP_NOTE" in
    "未检查遗留 ccclaw-run unit"|"未发现遗留 ccclaw-run unit")
      printf '%s\n' "$reason"
      ;;
    *)
      printf '%s；%s\n' "$reason" "$LEGACY_RUN_CLEANUP_NOTE"
      ;;
  esac
}

cleanup_legacy_user_systemd_run_units() {
  local legacy_service="$SYSTEMD_USER_DIR/ccclaw-run.service"
  local legacy_timer="$SYSTEMD_USER_DIR/ccclaw-run.timer"
  LEGACY_RUN_CLEANUP_NOTE="未发现遗留 ccclaw-run unit"
  if [[ ! -f "$legacy_service" && ! -f "$legacy_timer" ]]; then
    return 0
  fi
  if [[ "$SIMULATE" -eq 1 ]]; then
    if [[ "$SYSTEMD_CONTROL_READY" -eq 1 ]]; then
      log "[simulate] systemctl --user disable --now ccclaw-run.timer"
    fi
    log "[simulate] rm -f $legacy_timer $legacy_service"
    LEGACY_RUN_CLEANUP_NOTE="模拟模式：将清理遗留 ccclaw-run unit"
    return 0
  fi
  if [[ "$SYSTEMD_CONTROL_READY" -eq 1 ]]; then
    systemctl --user disable --now ccclaw-run.timer || true
  fi
  rm -f "$legacy_timer" "$legacy_service"
  if [[ "$SYSTEMD_CONTROL_READY" -eq 1 ]]; then
    LEGACY_RUN_CLEANUP_NOTE="已清理遗留 ccclaw-run unit"
    return 0
  fi
  LEGACY_RUN_CLEANUP_NOTE="已删除本地遗留 ccclaw-run unit 文件；请在登录会话执行 systemctl --user disable --now ccclaw-run.timer || true 与 systemctl --user daemon-reload"
}

reconcile_managed_crontab() {
  local message=""
  if [[ ! -x "$APP_DIR/bin/ccclaw" || ! -f "$CONFIG_FILE" ]]; then
    CRON_MANAGED_STATUS="skip"
    CRON_MANAGED_REASON="当前未发现可用 ccclaw 或配置文件，跳过受控 crontab 管理"
    return 0
  fi
  case "$SCHEDULER_EFFECTIVE" in
    systemd)
      if [[ "$SIMULATE" -eq 1 ]]; then
        CRON_MANAGED_STATUS="planned:disable"
        CRON_MANAGED_REASON="模拟模式：如存在 ccclaw 受控 crontab 规则将尝试清理"
        log "[simulate] $APP_DIR/bin/ccclaw --config $CONFIG_FILE --env-file $ENV_FILE scheduler disable-cron"
        return 0
      fi
      if message="$("$APP_DIR/bin/ccclaw" --config "$CONFIG_FILE" --env-file "$ENV_FILE" scheduler disable-cron 2>&1)"; then
        CRON_MANAGED_STATUS="disabled"
        CRON_MANAGED_REASON="$message"
        log "$message"
      else
        CRON_MANAGED_STATUS="warn"
        CRON_MANAGED_REASON="$message"
        warn "清理受控 crontab 失败: $message"
      fi
      ;;
    none)
      CRON_MANAGED_STATUS="skip(none)"
      CRON_MANAGED_REASON="当前调度模式为 none；安装器未自动改写 crontab。$(cron_manual_mode_hint | tr '\n' ' ')"
      ;;
  esac
}

activate_user_systemd_timers() {
  if [[ "$SCHEDULER_EFFECTIVE" != "systemd" ]]; then
    SYSTEMD_ACTIVATION_STATUS="skip($SCHEDULER_EFFECTIVE)"
    SYSTEMD_ACTIVATION_REASON="当前调度模式不是 systemd，跳过 user systemd 激活"
    return 0
  fi
  cleanup_legacy_user_systemd_run_units
  if [[ "$SYSTEMD_CONTROL_READY" -ne 1 ]]; then
    SYSTEMD_ACTIVATION_STATUS="manual"
    SYSTEMD_ACTIVATION_REASON="$(append_legacy_cleanup_note "当前会话未直连 user bus；需在登录会话手工执行 daemon-reload + enable --now")"
    return 0
  fi
  if [[ "$SIMULATE" -eq 1 ]]; then
    SYSTEMD_ACTIVATION_STATUS="planned:auto"
    SYSTEMD_ACTIVATION_REASON="$(append_legacy_cleanup_note "模拟模式：将自动执行 daemon-reload + enable --now")"
    log "[simulate] systemctl --user daemon-reload"
    log "[simulate] systemctl --user enable --now ccclaw-ingest.timer ccclaw-patrol.timer ccclaw-journal.timer ccclaw-archive.timer ccclaw-sevolver.timer"
    if [[ "$CONFIG_FILE_ALREADY_EXISTS" -eq 1 ]]; then
      log "[simulate] systemctl --user restart ccclaw-ingest.timer ccclaw-patrol.timer ccclaw-journal.timer ccclaw-archive.timer ccclaw-sevolver.timer"
    fi
    return 0
  fi
  systemctl --user daemon-reload
  systemctl --user enable --now ccclaw-ingest.timer ccclaw-patrol.timer ccclaw-journal.timer ccclaw-archive.timer ccclaw-sevolver.timer
  if [[ "$CONFIG_FILE_ALREADY_EXISTS" -eq 1 ]]; then
    systemctl --user restart ccclaw-ingest.timer ccclaw-patrol.timer ccclaw-journal.timer ccclaw-archive.timer ccclaw-sevolver.timer
    SYSTEMD_ACTIVATION_STATUS="restarted"
    SYSTEMD_ACTIVATION_REASON="$(append_legacy_cleanup_note "已自动 daemon-reload，并重启托管 timer 以加载更新后的 unit")"
    return 0
  fi
  SYSTEMD_ACTIVATION_STATUS="enabled"
  SYSTEMD_ACTIVATION_REASON="$(append_legacy_cleanup_note "已自动 daemon-reload，并启用托管 timer")"
}

remove_managed_crontab_only() {
  local message=""
  [[ -x "$APP_DIR/bin/ccclaw" ]] || fail "未找到已安装的 ccclaw: $APP_DIR/bin/ccclaw"
  [[ -f "$CONFIG_FILE" ]] || fail "未找到调度配置文件: $CONFIG_FILE"
  if ! message="$("$APP_DIR/bin/ccclaw" --config "$CONFIG_FILE" --env-file "$ENV_FILE" scheduler disable-cron 2>&1)"; then
    fail "$message"
  fi
  printf '%s\n' "$message"
}

copy_ops_tree() {
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] cp -R $DIST_DIR/ops/. $APP_DIR/ops/"
    return 0
  fi
  copy_release_dir_contents "$DIST_DIR/ops" "$APP_DIR/ops"
}

install_release_scripts() {
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] install $DIST_DIR/install.sh -> $APP_DIR/install.sh"
    log "[simulate] install $DIST_DIR/upgrade.sh -> $APP_DIR/upgrade.sh"
    return 0
  fi
  install_release_file 755 "$DIST_DIR/install.sh" "$APP_DIR/install.sh"
  install_release_file 755 "$DIST_DIR/upgrade.sh" "$APP_DIR/upgrade.sh"
}

create_app_layout() {
  ensure_dir "$APP_DIR/bin" "$APP_DIR/ops/config" "$APP_DIR/ops/systemd" "$APP_DIR/ops/scripts" "$APP_DIR/var" "$APP_DIR/log" "$HOME/.local/bin"
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] install $DIST_DIR/bin/ccclaw -> $APP_DIR/bin/ccclaw"
  else
    install_release_file 755 "$DIST_DIR/bin/ccclaw" "$APP_DIR/bin/ccclaw"
  fi
  install_release_scripts
  copy_ops_tree
  sync_app_readme
  sync_jj_quickref
  sync_croncfg_reference
  create_claude_wrapper
  create_env_file
  create_config_file
  create_user_systemd_units
  sync_scheduler_config
  reconcile_managed_crontab
  activate_user_systemd_timers
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] ln -sf $APP_DIR/bin/ccclaw $BIN_LINK"
  else
    ln -sf "$APP_DIR/bin/ccclaw" "$BIN_LINK"
  fi
}

seed_home_repo_tree() {
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] seed kb/docs tree into $HOME_REPO"
    return 0
  fi
  mkdir -p "$HOME_REPO/kb/designs" "$HOME_REPO/kb/assay" "$HOME_REPO/kb/journal" "$HOME_REPO/kb/skills/L1" "$HOME_REPO/kb/skills/L2" "$HOME_REPO/docs/reports" "$HOME_REPO/docs/plans" "$HOME_REPO/docs/rfcs"
  cp -R -n "$DIST_DIR/kb/." "$HOME_REPO/kb/"
  merge_kb_contracts
  if [[ ! -f "$HOME_REPO/README.md" ]]; then
    cat > "$HOME_REPO/README.md" <<'README'
# ccclaw-home

这是本机 `ccclaw` 的封闭本体仓库。

- `kb/` 保存长期记忆与 skills
- `docs/` 保存本体级计划、报告与架构文档
- 该仓库不参与程序升级覆盖
README
  fi
  if [[ ! -f "$HOME_REPO/CLAUDE.md" ]]; then
    cat > "$HOME_REPO/CLAUDE.md" <<'CLAUDE'
# CLAUDE.md

此目录是 `ccclaw` 的本体记忆仓库，不是程序发布树。

- 允许持续追加 `kb/` 与 `docs/` 内容
- 升级程序时不要覆盖这里的用户记忆
- 所有变更优先保持可追溯、可提交、可回滚
CLAUDE
  fi
  touch "$HOME_REPO/.gitignore"
  append_line_if_missing "$HOME_REPO/.gitignore" ".DS_Store"
  append_line_if_missing "$HOME_REPO/.gitignore" "*.swp"
  append_line_if_missing "$HOME_REPO/.gitignore" "*.tmp"
}

list_home_repo_seed_relative_files() {
  {
    if [[ -d "$DIST_DIR/kb" ]]; then
      while IFS= read -r f; do
        printf 'kb/%s\n' "${f#$DIST_DIR/kb/}"
      done < <(find "$DIST_DIR/kb" -type f -name 'CLAUDE.md' 2>/dev/null | sort)
    fi
    if [[ -d "$HOME_REPO/kb" ]]; then
      while IFS= read -r f; do
        printf '%s\n' "${f#$HOME_REPO/}"
      done < <(find "$HOME_REPO/kb" -type f -name 'CLAUDE.md' 2>/dev/null | sort)
    fi
    printf '%s\n' README.md CLAUDE.md .gitignore
  } | awk 'NF && !seen[$0]++' | sort
}

list_home_repo_refresh_relative_files() {
  if [[ -d "$DIST_DIR/kb" ]]; then
    find "$DIST_DIR/kb" -type f -name 'CLAUDE.md' 2>/dev/null | while IFS= read -r f; do
      printf 'kb/%s\n' "${f#$DIST_DIR/kb/}"
    done | sort
  fi
}

home_repo_commit_message() {
  local base_message="$1"
  if [[ -n "$CCCLAW_VERSION" ]]; then
    printf '%s (v%s)\n' "$base_message" "$CCCLAW_VERSION"
    return 0
  fi
  printf '%s\n' "$base_message"
}

print_home_repo_commit_followup() {
  local remote_name branch_name status_head ahead_count
  printf '\n== 本体仓库自动提交完成（仅受管模板文件）==\n'
  remote_name="$(git -C "$HOME_REPO" remote 2>/dev/null | head -n 1 || true)"
  if [[ -n "$remote_name" ]]; then
    status_head="$(git -C "$HOME_REPO" status --porcelain=v1 -b 2>/dev/null | head -n 1 || true)"
    ahead_count="$(printf '%s\n' "$status_head" | sed -nE 's/.*ahead ([0-9]+).*/\1/p')"
    if [[ -n "$ahead_count" && "$ahead_count" != "0" ]]; then
      log "[home_repo ahead $ahead_count，建议执行：git -C \"$HOME_REPO\" push]"
    else
      branch_name="$(git -C "$HOME_REPO" rev-parse --abbrev-ref HEAD 2>/dev/null || printf 'HEAD')"
      log "检测到本体仓库 remote=$remote_name，但当前分支 $branch_name 尚未返回 ahead 统计；首次推送建议执行："
      log "  git -C \"$HOME_REPO\" push -u $remote_name HEAD"
    fi
  else
    log "未检测到本体仓库 remote，跳过 ahead 提示。"
  fi
  log "查看当前状态："
  log "  git -C \"$HOME_REPO\" status"
  log "  git -C \"$HOME_REPO\" log --oneline -5"
}

commit_home_repo_seed() {
  local commit_message relative_path
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] git add/commit seeded files in $HOME_REPO"
    log "[simulate] 将要 add 的文件列表："
    while IFS= read -r relative_path; do
      log "[simulate]   - $relative_path"
    done < <(list_home_repo_seed_relative_files)
    return 0
  fi
  # 决策 #3（Issue#58）：提交前确保本地已有可用 git 工作复本
  if [[ ! -d "$HOME_REPO/.git" ]]; then
    warn "本体仓库不是 git 工作复本，跳过自动提交: $HOME_REPO"
    return 0
  fi
  # 决策 #2（Issue#58）：只限受管模板文件，排除运行态内容
  # 仅提交 kb/**/CLAUDE.md + README.md / CLAUDE.md / .gitignore
  while IFS= read -r relative_path; do
    git -C "$HOME_REPO" add -- "$relative_path" 2>/dev/null || true
  done < <(list_home_repo_seed_relative_files)
  if git -C "$HOME_REPO" diff --cached --quiet; then
    return 0
  fi
  commit_message="$(home_repo_commit_message 'seed ccclaw home repo')"
  git -C "$HOME_REPO" -c user.name='ccclaw' -c user.email='ccclaw@local' commit -m "$commit_message"
  log "已提交：kb/**/CLAUDE.md, README.md, CLAUDE.md, .gitignore"
  print_home_repo_commit_followup
}

commit_home_repo_refresh() {
  local commit_message relative_path
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] git add/commit refreshed files in $HOME_REPO"
    log "[simulate] 将要 add 的文件列表："
    while IFS= read -r relative_path; do
      log "[simulate]   - $relative_path"
    done < <(list_home_repo_refresh_relative_files)
    return 0
  fi
  [[ -d "$HOME_REPO/.git" ]] || fail "升级刷新要求 home_repo 已存在且为 git 仓库: $HOME_REPO"
  while IFS= read -r relative_path; do
    git -C "$HOME_REPO" add -- "$relative_path" 2>/dev/null || true
  done < <(list_home_repo_refresh_relative_files)
  if git -C "$HOME_REPO" diff --cached --quiet; then
    return 0
  fi
  commit_message="$(home_repo_commit_message 'seed ccclaw home repo')"
  git -C "$HOME_REPO" -c user.name='ccclaw' -c user.email='ccclaw@local' commit -m "$commit_message"
  log "已提交：kb/**/CLAUDE.md"
  print_home_repo_commit_followup
}

init_jj_colocate_repo() {
  local repo_path="$1"
  [[ -n "$repo_path" ]] || return 0
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] jj git init --colocate $repo_path"
    return 0
  fi
  [[ -d "$repo_path/.git" ]] || return 0
  [[ ! -d "$repo_path/.jj" ]] || return 0
  log "初始化 jj colocated 仓库: $repo_path"
  jj git init --colocate "$repo_path"
}

preview_home_repo_simulation() {
  case "$HOME_REPO_MODE" in
    init)
      log "[simulate] initialize home repo at $HOME_REPO"
      ;;
    remote)
      [[ -n "$HOME_REPO_REMOTE" ]] || fail "本体 remote 模式缺少远程仓库"
      log "[simulate] git clone $(clone_url_from_repo_input "$HOME_REPO_REMOTE") $HOME_REPO"
      ;;
    local)
      log "[simulate] attach local home repo at $HOME_REPO"
      ;;
  esac
  seed_home_repo_tree
  commit_home_repo_seed
  init_jj_colocate_repo "$HOME_REPO"
}

preview_home_repo_upgrade_simulation() {
  log "[simulate] refresh managed home repo templates at $HOME_REPO"
  merge_kb_contracts
  commit_home_repo_refresh
  init_jj_colocate_repo "$HOME_REPO"
}

init_or_attach_home_repo() {
  case "$HOME_REPO_MODE" in
    init)
      if [[ "$SIMULATE" -eq 1 ]]; then
        log "[simulate] initialize home repo at $HOME_REPO"
        return 0
      fi
      if [[ ! -d "$HOME_REPO" ]]; then
        run_maybe_sudo_for_path "$HOME_REPO" mkdir -p "$HOME_REPO"
      fi
      ensure_path_owner "$HOME_REPO"
      if [[ ! -d "$HOME_REPO/.git" ]]; then
        log "初始化本体仓库: $HOME_REPO"
        git -C "$HOME_REPO" init
      fi
      ;;
    remote)
      [[ -n "$HOME_REPO_REMOTE" ]] || fail "本体 remote 模式缺少远程仓库"
      if [[ "$SIMULATE" -eq 1 ]]; then
        log "[simulate] git clone $(clone_url_from_repo_input "$HOME_REPO_REMOTE") $HOME_REPO"
        return 0
      fi
      if [[ -e "$HOME_REPO" ]] && dir_has_entries "$HOME_REPO"; then
        fail "本体 remote 模式要求目标目录为空: $HOME_REPO"
      fi
      run_maybe_sudo_for_path "$HOME_REPO" mkdir -p "$(dirname "$HOME_REPO")"
      run_maybe_sudo_for_path "$HOME_REPO" git clone "$(clone_url_from_repo_input "$HOME_REPO_REMOTE")" "$HOME_REPO"
      ensure_path_owner "$HOME_REPO"
      ;;
    local)
      if [[ "$SIMULATE" -eq 1 ]]; then
        log "[simulate] attach local home repo at $HOME_REPO"
        return 0
      fi
      [[ -d "$HOME_REPO/.git" ]] || fail "本地本体仓库不是 git 仓库: $HOME_REPO"
      ensure_path_owner "$HOME_REPO"
      ;;
  esac
  seed_home_repo_tree
  commit_home_repo_seed
  init_jj_colocate_repo "$HOME_REPO"
}

refresh_managed_home_repo_templates() {
  [[ -d "$HOME_REPO" ]] || fail "升级刷新要求现有 home_repo 目录存在: $HOME_REPO"
  [[ -d "$HOME_REPO/.git" ]] || fail "升级刷新要求 home_repo 已存在且为 git 仓库: $HOME_REPO"
  merge_kb_contracts
  commit_home_repo_refresh
  init_jj_colocate_repo "$HOME_REPO"
}

prepare_task_clone_root() {
  if [[ "$TASK_REPO_MODE" != "remote" ]]; then
    return 0
  fi
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] ensure remote clone root $TASK_CLONE_ROOT"
    return 0
  fi
  run_maybe_sudo_for_path "$TASK_CLONE_ROOT" mkdir -p "$TASK_CLONE_ROOT"
  ensure_path_owner "$TASK_CLONE_ROOT"
}

bind_task_repo() {
  local target_args
  case "$TASK_REPO_MODE" in
    none)
      return 0
      ;;
    remote)
      prepare_task_clone_root
      if [[ "$SIMULATE" -eq 1 ]]; then
        log "[simulate] git clone $(clone_url_from_repo_input "$TASK_REPO_REMOTE") $TASK_REPO_PATH"
      else
        if [[ -e "$TASK_REPO_PATH" ]] && dir_has_entries "$TASK_REPO_PATH"; then
          fail "任务 remote 模式要求目标目录为空: $TASK_REPO_PATH"
        fi
        run_maybe_sudo_for_path "$TASK_REPO_PATH" mkdir -p "$(dirname "$TASK_REPO_PATH")"
        run_maybe_sudo_for_path "$TASK_REPO_PATH" git clone "$(clone_url_from_repo_input "$TASK_REPO_REMOTE")" "$TASK_REPO_PATH"
        ensure_path_owner "$TASK_REPO_PATH"
      fi
      ;;
    local)
      [[ -d "$TASK_REPO_LOCAL/.git" ]] || fail "本地任务仓库不是 git 仓库: $TASK_REPO_LOCAL"
      TASK_REPO_PATH="$TASK_REPO_LOCAL"
      ;;
  esac

  target_args=(target add --config "$CONFIG_FILE" --repo "$TASK_REPO" --path "$TASK_REPO_PATH")
  if [[ -n "$TASK_KB_PATH" ]]; then
    target_args+=(--kb-path "$TASK_KB_PATH")
  fi
  if ! config_has_targets; then
    target_args+=(--default)
  fi
  if [[ "$SIMULATE" -eq 1 ]]; then
    log "[simulate] $APP_DIR/bin/ccclaw ${target_args[*]}"
    return 0
  fi
  init_jj_colocate_repo "$TASK_REPO_PATH"
  "$APP_DIR/bin/ccclaw" "${target_args[@]}"
}

print_summary() {
  local installed_version="unknown"
  local task_summary="未绑定"
  local home_source_summary="$HOME_REPO_MODE"
  local gh_token_summary="$GH_TOKEN_SOURCE"
  local headline="安装完成。"
  local result_title="当前主机部署成果"
  local scheduler_step_6=""
  local scheduler_step_7=""
  local cron_summary="$CRON_MANAGED_REASON"
  if [[ -x "$APP_DIR/bin/ccclaw" ]]; then
    installed_version="$("$APP_DIR/bin/ccclaw" -V 2>/dev/null || echo unknown)"
  fi
  if [[ "$PREFLIGHT_ONLY" -eq 1 ]]; then
    headline="体检完成，未写入文件。"
    result_title="当前主机体检结果"
    cron_summary="体检模式未写入受控 crontab"
  elif [[ "$SIMULATE" -eq 1 ]]; then
    headline="模拟执行完成，未写入文件。"
    result_title="当前主机模拟结果"
    cron_summary="模拟模式未写入受控 crontab"
  fi
  if [[ "$gh_token_summary" == "未写入" && -n "$GH_TOKEN_DETECTED" ]]; then
    gh_token_summary="待从 gh auth token 回填"
  fi
  if [[ "$UPGRADE_HOME_REPO" -eq 1 ]]; then
    home_source_summary="upgrade-refresh -> $HOME_REPO"
  else
    case "$HOME_REPO_MODE" in
      remote) home_source_summary="remote -> $HOME_REPO_REMOTE" ;;
      local) home_source_summary="local -> $HOME_REPO" ;;
      init) home_source_summary="init -> $HOME_REPO" ;;
    esac
  fi
  case "$TASK_REPO_MODE" in
    remote) task_summary="$TASK_REPO @ $TASK_REPO_PATH (from $TASK_REPO_REMOTE)" ;;
    local) task_summary="$TASK_REPO @ $TASK_REPO_PATH (local)" ;;
  esac
  case "$SCHEDULER_EFFECTIVE" in
    systemd)
      if [[ "$SYSTEMD_CONTROL_READY" -eq 1 ]]; then
        scheduler_step_6="6. 当前会话可直连 user bus；安装/升级已自动完成 daemon-reload 与 timer 启用。
   可复核:
   $APP_DIR/bin/ccclaw scheduler timers --config $CONFIG_FILE --env-file $ENV_FILE"
      else
        scheduler_step_6="6. 当前会话未直连 user bus；请在登录会话中手工启用用户定时器:
   systemctl --user disable --now ccclaw-run.timer || true
   systemctl --user daemon-reload
   systemctl --user enable --now ccclaw-ingest.timer ccclaw-patrol.timer ccclaw-journal.timer ccclaw-archive.timer ccclaw-sevolver.timer"
      fi
      scheduler_step_7="7. 若需要清理历史托管 cron 规则，可执行:
   $APP_DIR/bin/ccclaw --config $CONFIG_FILE --env-file $ENV_FILE scheduler disable-cron
   详细专家说明见: $CRONCFG_FILE"
      ;;
    none|*)
      scheduler_step_6="6. 当前调度模式为 none；安装器未自动写入 crontab。
$(cron_manual_summary)"
      scheduler_step_7="7. 若后续修复好 user systemd，再执行:
   $APP_DIR/bin/ccclaw --config $CONFIG_FILE --env-file $ENV_FILE scheduler disable-cron
   systemctl --user daemon-reload
   systemctl --user enable --now ccclaw-ingest.timer ccclaw-patrol.timer ccclaw-journal.timer ccclaw-archive.timer ccclaw-sevolver.timer"
      ;;
  esac
  cat <<MSG
$headline

$result_title
- 版本: $installed_version
- 程序目录: $APP_DIR
- 本体仓库: $HOME_REPO
- 本体仓库来源: $home_source_summary
- 可执行文件: $APP_DIR/bin/ccclaw
- 执行器包装: $CLAUDE_WRAPPER
- 隐私配置: $ENV_FILE
- 程序手册: $APP_DIR/README.md
- GH_TOKEN 来源: $gh_token_summary
- Claude 凭据态: $CLAUDE_AUTH_METHOD
- Claude 生态处理: 默认只读探查，未自动修改 marketplace/plugins/rtk 全局配置
- 普通配置: $CONFIG_FILE
- 本地命令链接: $BIN_LINK
- shell 集成: $SHELL_INTEGRATION_STATUS
- shell 集成说明: $SHELL_INTEGRATION_REASON
- 任务仓库绑定: $task_summary
- 调度器: 请求=$SCHEDULER, 生效=$SCHEDULER_EFFECTIVE
- 调度器说明: $SCHEDULER_REASON
- user systemd 激活: $SYSTEMD_ACTIVATION_REASON
- user systemd 单元目录: $SYSTEMD_USER_DIR
- user systemd 单元(仅 systemd 模式写入):
  - ccclaw-ingest.service
  - ccclaw-ingest.timer
  - ccclaw-patrol.service
  - ccclaw-patrol.timer
  - ccclaw-journal.service
  - ccclaw-journal.timer
  - ccclaw-archive.service
  - ccclaw-archive.timer
- 受控 crontab: $cron_summary

建议下一步
1. 检查 $ENV_FILE 中的敏感项是否齐全
2. 检查 $CONFIG_FILE 中的 repo/path 是否正确
3. 进入本体仓库: cd $HOME_REPO && git status
4. 运行一次体检:
   $APP_DIR/bin/ccclaw doctor --config $CONFIG_FILE --env-file $ENV_FILE
5. 若需要补绑任务仓库:
   $APP_DIR/bin/ccclaw target list --config $CONFIG_FILE
$scheduler_step_6
$scheduler_step_7
8. 若本体仓库目前还没有 upstream，可按需执行:
   git -C $HOME_REPO remote add origin <repo-url>
   git -C $HOME_REPO branch -M main
   git -C $HOME_REPO push -u origin main
9. 若任务仓库后续迁移目录，请同步修改 $CONFIG_FILE 中对应 target 的 local_path

日常使用流程
1. 绑定工作任务仓库
2. 在控制仓库创建或维护 Issue
3. 检验工作成果，并继续在 Issue 中回复交流

开源协作流程
1. maintain 及以上成员创建的 Issue 会自动进入执行判定
2. 外部成员 Issue 默认只巡查与讨论，不自动执行
3. 值得接受的提案，由受信任成员评论 /ccclaw <批准词> 后进入执行；最新评论也可用否决词撤回
MSG
}

main() {
  validate_shell_options
  load_existing_scheduler_preferences
  if [[ "$REMOVE_SHELL_INTEGRATION" != "none" ]]; then
    remove_shell_integration "$REMOVE_SHELL_INTEGRATION"
    exit 0
  fi
  if [[ "$REMOVE_CRON" -eq 1 ]]; then
    remove_managed_crontab_only
    exit 0
  fi
  probe_claude
  collect_inputs
  validate_inputs
  plan_shell_integration
  if [[ "$TASK_REPO_MODE" == "local" && -z "$TASK_REPO_PATH" ]]; then
    TASK_REPO_PATH="$TASK_REPO_LOCAL"
  fi
  print_flow
  print_preflight
  if [[ "$PREFLIGHT_ONLY" -eq 1 ]]; then
    print_summary
    exit 0
  fi
  if [[ "$SIMULATE" -eq 1 ]]; then
    if [[ "$UPGRADE_HOME_REPO" -eq 1 ]]; then
      preview_home_repo_upgrade_simulation
    else
      preview_home_repo_simulation
    fi
    print_summary
    exit 0
  fi
  ensure_system_packages
  install_claude_official
  install_rtk
  create_app_layout
  apply_shell_integration "$SHELL_INTEGRATION"
  if [[ "$UPGRADE_HOME_REPO" -eq 1 ]]; then
    refresh_managed_home_repo_templates
  else
    init_or_attach_home_repo
  fi
  bind_task_repo
  print_summary
}

if [[ "${CCCLAW_INSTALL_LIB_ONLY:-0}" == "1" ]]; then
  return 0 2>/dev/null || exit 0
fi

main "$@"
