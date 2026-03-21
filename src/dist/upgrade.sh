#!/usr/bin/env bash
set -euo pipefail

CONTROL_REPO="41490/ccclaw"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
APP_DIR_EXPLICIT=0
HOME_REPO_EXPLICIT=0
if [[ -n "${APP_DIR+x}" ]]; then
  APP_DIR_EXPLICIT=1
fi
if [[ -n "${HOME_REPO+x}" ]]; then
  HOME_REPO_EXPLICIT=1
fi
APP_DIR="${APP_DIR:-$SCRIPT_DIR}"
HOME_REPO="${HOME_REPO:-}"
CONFIG_FILE="${CONFIG_FILE:-$APP_DIR/ops/config/config.toml}"
UPGRADE_PROGRAM="${UPGRADE_PROGRAM:-1}"
UPGRADE_CLAUDE="${UPGRADE_CLAUDE:-0}"
REFRESH_CLAUDE_ASSETS="${REFRESH_CLAUDE_ASSETS:-0}"
WORK_DIR=""
RELEASE_TAG=""

log() { printf '[ccclaw-upgrade] %s\n' "$*"; }
fail() { printf '[ccclaw-upgrade][FAIL] %s\n' "$*" >&2; exit 1; }

cleanup() {
  if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
    rm -rf "$WORK_DIR"
  fi
}
trap cleanup EXIT

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

load_existing_context() {
  local value=""
  [[ -f "$CONFIG_FILE" ]] || return 0
  if [[ "$APP_DIR_EXPLICIT" -eq 0 ]]; then
    value="$(toml_get_value "$CONFIG_FILE" "paths" "app_dir")"
    if [[ -n "$value" ]]; then
      APP_DIR="$value"
    fi
  fi
  if [[ "$HOME_REPO_EXPLICIT" -eq 0 ]]; then
    value="$(toml_get_value "$CONFIG_FILE" "paths" "home_repo")"
    if [[ -n "$value" ]]; then
      HOME_REPO="$value"
    fi
  fi
}

require_tool() {
  local tool="$1"
  command -v "$tool" >/dev/null 2>&1 || fail "缺少必要命令: $tool"
}

detect_platform() {
  local uname_s uname_m
  uname_s="$(uname -s)"
  uname_m="$(uname -m)"
  case "$uname_s" in
    Linux) GOOS="linux" ;;
    *) fail "暂不支持当前系统: $uname_s" ;;
  esac
  case "$uname_m" in
    x86_64|amd64) GOARCH="amd64" ;;
    aarch64|arm64) GOARCH="arm64" ;;
    *) fail "暂不支持当前架构: $uname_m" ;;
  esac
}

download_latest_release() {
  local package_name checksum_file archive_file
  require_tool gh
  require_tool tar
  require_tool sha256sum

  RELEASE_TAG="$(gh release view --repo "$CONTROL_REPO" --json tagName --jq '.tagName')"
  [[ -n "$RELEASE_TAG" ]] || fail "无法获取最新 release tag"

  PACKAGE_NAME="ccclaw_${RELEASE_TAG}_${GOOS}_${GOARCH}.tar.gz"
  CHECKSUM_NAME="SHA256SUMS"
  WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/ccclaw-upgrade.XXXXXX")"
  DOWNLOAD_DIR="$WORK_DIR/download"
  EXTRACT_DIR="$WORK_DIR/extract"
  mkdir -p "$DOWNLOAD_DIR" "$EXTRACT_DIR"

  log "下载官方 release: repo=$CONTROL_REPO tag=$RELEASE_TAG asset=$PACKAGE_NAME"
  gh release download "$RELEASE_TAG" \
    --repo "$CONTROL_REPO" \
    --dir "$DOWNLOAD_DIR" \
    --pattern "$PACKAGE_NAME" \
    --pattern "$CHECKSUM_NAME" >/dev/null

  checksum_file="$DOWNLOAD_DIR/$CHECKSUM_NAME"
  archive_file="$DOWNLOAD_DIR/$PACKAGE_NAME"
  [[ -f "$checksum_file" ]] || fail "缺少校验文件: $checksum_file"
  [[ -f "$archive_file" ]] || fail "缺少安装包: $archive_file"
  grep -Fq " $PACKAGE_NAME" "$checksum_file" || fail "SHA256SUMS 未包含当前安装包: $PACKAGE_NAME"

  log "校验 SHA256SUMS"
  (
    cd "$DOWNLOAD_DIR"
    sha256sum -c "$CHECKSUM_NAME"
  )

  tar -tzf "$archive_file" > "$WORK_DIR/archive.list"
  RELEASE_ROOT="$(head -n 1 "$WORK_DIR/archive.list" | cut -d/ -f1)"
  [[ -n "$RELEASE_ROOT" ]] || fail "无法识别安装包根目录"

  tar -C "$EXTRACT_DIR" -xzf "$archive_file"
  RELEASE_DIR="$EXTRACT_DIR/$RELEASE_ROOT"
  [[ -x "$RELEASE_DIR/install.sh" ]] || fail "release 中缺少 install.sh: $RELEASE_DIR/install.sh"
}

run_release_installer() {
  local args=()
  args=(--yes --app-dir "$APP_DIR" --home-repo "$HOME_REPO" --upgrade-home-repo)
  if [[ "$UPGRADE_CLAUDE" == "1" ]]; then
    args+=(--install-claude)
  fi
  CCCLAW_VERSION="$RELEASE_TAG" "$RELEASE_DIR/install.sh" "${args[@]}"
}

load_existing_context
if [[ -z "$HOME_REPO" ]]; then
  HOME_REPO="/opt/ccclaw"
fi
detect_platform

echo "升级分轨:"
echo "- 官方控制仓库: $CONTROL_REPO"
echo "- 程序发布树: $([[ "$UPGRADE_PROGRAM" == "1" ]] && echo enabled || echo skipped)"
echo "- Claude 安装: $([[ "$UPGRADE_CLAUDE" == "1" ]] && echo enabled || echo skipped)"
echo "- Claude 资产自动刷新: disabled(默认只读策略，已停用)"
echo "- 本体仓库保护: 不覆盖用户记忆；仅无损刷新关键 kb/CLAUDE.md 受管区块"
echo "- jj / Skill 基线: 升级时刷新受管 Skill，并补齐 jj colocated / 默认 bookmark 跟踪"
echo "- 当前程序目录: $APP_DIR"
echo "- 当前本体仓库: $HOME_REPO"

if [[ "$UPGRADE_PROGRAM" == "1" ]]; then
  download_latest_release
  run_release_installer
fi

if [[ "$REFRESH_CLAUDE_ASSETS" == "1" ]]; then
  echo "REFRESH_CLAUDE_ASSETS=1 已不再触发任何自动 marketplace/plugin/rtk 改动；如需修改 Claude 资产，请手工执行。"
fi

echo "升级完成。若当前会话未能自动直连 user bus，请在登录会话中执行: systemctl --user daemon-reload && systemctl --user restart ccclaw-ingest.timer ccclaw-patrol.timer ccclaw-journal.timer"
