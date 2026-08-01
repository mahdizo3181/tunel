#!/usr/bin/env bash
#
# tunel installer / management dashboard.
#
# Run on a fresh VPS as root:
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/YOUR_GITHUB_USERNAME/tunel/main/install.sh)
#
# (process substitution, not a `curl | bash` pipe, so stdin stays free for
# the interactive prompts below). On a machine that already has the repo
# cloned, `cd /root/tunel && ./install.sh` re-opens the same dashboard.

set -uo pipefail

# ============================================================
# EDIT THIS before pushing to your own GitHub repository, or
# override at invocation time with TUNEL_REPO_URL=... bash <(...)
# ============================================================
REPO_URL="${TUNEL_REPO_URL:-https://github.com/YOUR_GITHUB_USERNAME/tunel.git}"
BRANCH="${TUNEL_BRANCH:-main}"
INSTALL_DIR="${TUNEL_INSTALL_DIR:-/root/tunel}"
# Must match the uid the "gate" user is created with in Dockerfile.server.
GATE_UID=10001

# ---------- output helpers ----------
if [[ -t 1 ]]; then
	C_RESET=$'\033[0m'; C_BOLD=$'\033[1m'
	C_RED=$'\033[0;31m'; C_GREEN=$'\033[0;32m'; C_YELLOW=$'\033[0;33m'
	C_BLUE=$'\033[0;34m'; C_CYAN=$'\033[0;36m'
else
	C_RESET=''; C_BOLD=''; C_RED=''; C_GREEN=''; C_YELLOW=''; C_BLUE=''; C_CYAN=''
fi

log()  { echo -e "${C_CYAN}[*]${C_RESET} $*"; }
ok()   { echo -e "${C_GREEN}[OK]${C_RESET} $*"; }
warn() { echo -e "${C_YELLOW}[!]${C_RESET} $*"; }
err()  { echo -e "${C_RED}[ERROR]${C_RESET} $*" >&2; }
die()  { err "$*"; exit 1; }

# ---------- preflight ----------
require_root() {
	if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
		die "Please run as root, e.g.: sudo bash <(curl -fsSL <raw-install.sh-url>)"
	fi
}

detect_os() {
	[[ -f /etc/os-release ]] || die "Cannot detect OS (missing /etc/os-release). Ubuntu/Debian required."
	# shellcheck disable=SC1091
	. /etc/os-release
	case "${ID:-}:${ID_LIKE:-}" in
		*ubuntu*|*debian*) : ;;
		*) die "Unsupported OS: ${PRETTY_NAME:-unknown}. This installer supports Ubuntu/Debian only." ;;
	esac
}

# ---------- dependencies ----------
COMPOSE_CMD=""

detect_compose_cmd() {
	if docker compose version >/dev/null 2>&1; then
		COMPOSE_CMD="docker compose"
	elif command -v docker-compose >/dev/null 2>&1; then
		COMPOSE_CMD="docker-compose"
	else
		COMPOSE_CMD=""
	fi
}

install_dependencies() {
	log "Checking dependencies (git, curl, openssl, docker, compose)..."

	local pkgs=()
	command -v git     >/dev/null 2>&1 || pkgs+=(git)
	command -v curl    >/dev/null 2>&1 || pkgs+=(curl)
	command -v openssl >/dev/null 2>&1 || pkgs+=(openssl)

	if [[ ${#pkgs[@]} -gt 0 ]]; then
		log "Installing: ${pkgs[*]}"
		apt-get update -y || die "apt-get update failed"
		apt-get install -y "${pkgs[@]}" || die "Failed to install: ${pkgs[*]}"
	fi

	if ! command -v docker >/dev/null 2>&1; then
		log "Installing Docker Engine from Docker's official apt repository..."
		apt-get update -y
		apt-get install -y ca-certificates curl gnupg
		install -m 0755 -d /etc/apt/keyrings
		# shellcheck disable=SC1091
		. /etc/os-release
		curl -fsSL "https://download.docker.com/linux/${ID}/gpg" -o /etc/apt/keyrings/docker.asc \
			|| die "Failed to fetch Docker's GPG key"
		chmod a+r /etc/apt/keyrings/docker.asc
		echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${ID} ${VERSION_CODENAME} stable" \
			> /etc/apt/sources.list.d/docker.list
		apt-get update -y
		apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin \
			|| die "Failed to install Docker"
		if command -v systemctl >/dev/null 2>&1; then
			systemctl enable --now docker
		else
			service docker start || true
		fi
	fi

	detect_compose_cmd
	if [[ -z "$COMPOSE_CMD" ]]; then
		log "Installing the docker compose plugin..."
		apt-get update -y
		apt-get install -y docker-compose-plugin || apt-get install -y docker-compose
		detect_compose_cmd
	fi
	[[ -n "$COMPOSE_CMD" ]] || die "Could not find or install docker compose."

	ok "Dependencies ready (compose: $COMPOSE_CMD)"
}

# ---------- repo management ----------
clone_or_update_repo() {
	if [[ -d "$INSTALL_DIR/.git" ]]; then
		log "Existing installation found at $INSTALL_DIR — updating to latest $BRANCH."
		warn "This resets tracked files to origin/$BRANCH; your .env / config.*.json / certs are untracked and untouched."
		git -C "$INSTALL_DIR" fetch --depth 1 origin "$BRANCH" || die "git fetch failed"
		git -C "$INSTALL_DIR" reset --hard "origin/$BRANCH" || die "git reset failed"
		ok "Repository updated."
	else
		log "Cloning $REPO_URL into $INSTALL_DIR ..."
		mkdir -p "$(dirname "$INSTALL_DIR")"
		git clone --depth 1 --branch "$BRANCH" "$REPO_URL" "$INSTALL_DIR" || die "git clone failed"
		ok "Repository cloned."
	fi
	cd "$INSTALL_DIR" || die "Cannot cd into $INSTALL_DIR"
}

require_repo() {
	cd "$INSTALL_DIR" 2>/dev/null || die "Repo not found at $INSTALL_DIR yet — restart the installer."
	[[ -f docker-compose.yml ]] || die "docker-compose.yml missing in $INSTALL_DIR — clone may have failed."
}

# ---------- small input helpers ----------
random_token() {
	openssl rand -hex 24
}

# prompt <var> <question> [default]
prompt() {
	local __var="$1" __q="$2" __def="${3:-}" __ans
	if [[ -n "$__def" ]]; then
		read -r -p "$(printf '%b' "${C_BOLD}${__q}${C_RESET} [${__def}]: ")" __ans
		__ans="${__ans:-$__def}"
	else
		while true; do
			read -r -p "$(printf '%b' "${C_BOLD}${__q}${C_RESET}: ")" __ans
			[[ -n "$__ans" ]] && break
			warn "This field is required."
		done
	fi
	printf -v "$__var" '%s' "$__ans"
}

# prompt_port <var> <question> <default>
prompt_port() {
	local __var="$1" __q="$2" __def="$3" __ans
	while true; do
		read -r -p "$(printf '%b' "${C_BOLD}${__q}${C_RESET} [${__def}]: ")" __ans
		__ans="${__ans:-$__def}"
		if [[ "$__ans" =~ ^[0-9]+$ ]] && (( __ans >= 1 && __ans <= 65535 )); then
			printf -v "$__var" '%s' "$__ans"
			return
		fi
		warn "Enter a valid port number (1-65535)."
	done
}

confirm() {
	local __q="$1" __def="${2:-n}" __ans __hint="y/N"
	[[ "$__def" == "y" ]] && __hint="Y/n"
	read -r -p "$(printf '%b' "${C_BOLD}${__q}${C_RESET} [${__hint}]: ")" __ans
	__ans="${__ans:-$__def}"
	[[ "$__ans" =~ ^[Yy] ]]
}

normalize_path() {
	# ensure a leading slash on a WS path
	local p="$1"
	[[ "$p" == /* ]] || p="/$p"
	printf '%s' "$p"
}

write_env_token() {
	local token="$1" env_file="$INSTALL_DIR/.env"
	touch "$env_file"
	if grep -q '^TUNNEL_AUTH_TOKEN=' "$env_file" 2>/dev/null; then
		sed -i "s|^TUNNEL_AUTH_TOKEN=.*|TUNNEL_AUTH_TOKEN=${token}|" "$env_file"
	else
		echo "TUNNEL_AUTH_TOKEN=${token}" >> "$env_file"
	fi
}

# ---------- server (gate) setup ----------
setup_server() {
	require_repo
	echo
	echo -e "${C_BLUE}${C_BOLD}=== Server (Gate) setup — restricted region ===${C_RESET}"
	echo

	local listen_port fwd_port token ws_path

	prompt_port listen_port "Tunnel listen port (WSS; must match the Bridge's Server Port)" "443"
	prompt_port fwd_port "Public forwarding port (where your users / VPN clients connect)" "8443"
	prompt ws_path "WebSocket path" "/ws-$(random_token | cut -c1-8)"
	ws_path="$(normalize_path "$ws_path")"

	local gen_token; gen_token="$(random_token)"
	prompt token "Secret token (shared with the Bridge, press enter to auto-generate)" "$gen_token"

	echo
	echo "TLS mode for the origin:"
	echo "  1) Cloudflare Origin CA certificate (recommended — SSL/TLS mode = Full (strict))"
	echo "  2) Self-signed certificate (SSL/TLS mode = Full, NOT strict)"
	echo "  3) No TLS / plain HTTP (CDN edge terminates TLS, SSL/TLS mode = Flexible — not recommended)"
	local tls_choice
	read -r -p "Choose [1-3] (default 2): " tls_choice
	tls_choice="${tls_choice:-2}"

	mkdir -p "$INSTALL_DIR/certs"
	local cert_file="" key_file=""
	case "$tls_choice" in
		1)
			local src_cert src_key
			prompt src_cert "Path to the Origin CA certificate (.pem)" ""
			prompt src_key  "Path to the matching private key (.pem)" ""
			[[ -f "$src_cert" ]] || die "Certificate file not found: $src_cert"
			[[ -f "$src_key"  ]] || die "Key file not found: $src_key"
			cp "$src_cert" "$INSTALL_DIR/certs/origin.pem"
			cp "$src_key"  "$INSTALL_DIR/certs/origin-key.pem"
			cert_file="/certs/origin.pem"
			key_file="/certs/origin-key.pem"
			;;
		3)
			warn "Running without TLS on the origin. Only do this behind a CDN in Flexible mode."
			;;
		*)
			log "Generating a self-signed certificate..."
			openssl req -x509 -newkey rsa:2048 -keyout "$INSTALL_DIR/certs/origin-key.pem" \
				-out "$INSTALL_DIR/certs/origin.pem" -days 3650 -nodes -subj "/CN=gate" >/dev/null 2>&1
			cert_file="/certs/origin.pem"
			key_file="/certs/origin-key.pem"
			ok "Self-signed cert written to certs/origin.pem"
			;;
	esac

	if [[ -n "$cert_file" ]]; then
		# The gate container reads these read-only as its unprivileged
		# in-container user (uid GATE_UID, see Dockerfile.server); bind
		# mounts don't translate UIDs, so the host-side files need to be
		# owned by that same uid rather than made world-readable.
		chown "${GATE_UID}:${GATE_UID}" "$INSTALL_DIR/certs/origin.pem" "$INSTALL_DIR/certs/origin-key.pem" \
			|| warn "Could not chown cert files to uid ${GATE_UID} — the gate container may fail to read them."
		chmod 640 "$INSTALL_DIR/certs/origin.pem" "$INSTALL_DIR/certs/origin-key.pem"
	fi

	cat > "$INSTALL_DIR/config.server.json" <<EOF
{
  "listen_addr": "0.0.0.0:${listen_port}",
  "tls_cert_file": "${cert_file}",
  "tls_key_file": "${key_file}",
  "ws_path": "${ws_path}",
  "auth_header": "X-Tunnel-Auth",
  "auth_token": "",
  "forward_listen_addr": "0.0.0.0:${fwd_port}",
  "decoy_html_path": "",
  "padding": {"min_pad_bytes": 16, "max_pad_bytes": 256, "min_chunk": 1024, "max_chunk": 8192},
  "ping_interval_seconds": 10,
  "pong_timeout_seconds": 20,
  "yamux_keepalive_seconds": 15,
  "tcp_buffer_bytes": 4194304,
  "session_wait_millis": 5000
}
EOF

	write_env_token "$token"
	ok "Server config written to $INSTALL_DIR/config.server.json"
	echo
	echo -e "${C_YELLOW}Give these to the Bridge machine:${C_RESET}"
	echo "  Server Port : $listen_port"
	echo "  WS Path     : $ws_path"
	echo "  Token       : $token"
	echo
}

# ---------- client (bridge) setup ----------
setup_client() {
	require_repo
	echo
	echo -e "${C_BLUE}${C_BOLD}=== Client (Bridge) setup — unrestricted region ===${C_RESET}"
	echo

	local server_ip server_port ws_path token target_addr skip_verify="false"

	prompt server_ip "Server address (your CDN-fronted domain, or the Gate's IP)" ""
	prompt_port server_port "Server port (must match the Gate's tunnel listen port)" "443"
	prompt ws_path "WebSocket path (must match the Gate's ws_path)" "/ws"
	ws_path="$(normalize_path "$ws_path")"
	prompt token "Secret token (must match the Gate)" ""
	prompt target_addr "Local target to forward to (e.g. your VPN panel)" "127.0.0.1:1194"

	if confirm "Skip TLS certificate verification? (only for self-signed certs / bare-IP setups, never for a real CDN domain)" "n"; then
		skip_verify="true"
	fi

	cat > "$INSTALL_DIR/config.client.json" <<EOF
{
  "server_url": "wss://${server_ip}:${server_port}${ws_path}",
  "sni": "${server_ip}",
  "auth_header": "X-Tunnel-Auth",
  "auth_token": "",
  "target_addr": "${target_addr}",
  "insecure_skip_verify": ${skip_verify},
  "padding": {"min_pad_bytes": 16, "max_pad_bytes": 256, "min_chunk": 1024, "max_chunk": 8192},
  "ping_interval_seconds": 10,
  "pong_timeout_seconds": 20,
  "yamux_keepalive_seconds": 15,
  "tcp_buffer_bytes": 4194304,
  "reconnect_min_backoff_millis": 1000,
  "reconnect_max_backoff_millis": 30000,
  "stable_conn_millis": 60000
}
EOF

	write_env_token "$token"
	ok "Client config written to $INSTALL_DIR/config.client.json"
	echo
}

# ---------- service control ----------
start_service() {
	local svc="$1"
	require_repo
	[[ -f "$INSTALL_DIR/.env" ]] || die "No .env found — run setup first (menu option 1 or 2)."
	log "Building and starting '$svc'..."
	( cd "$INSTALL_DIR" && $COMPOSE_CMD up -d --build "$svc" ) || die "Failed to start $svc"
	ok "$svc is up. Use option [3] to view logs."
}

start_server_service() { start_service "gate"; }
start_client_service() { start_service "bridge"; }

view_logs() {
	require_repo
	echo "Which service?"
	echo "  1) gate (server)"
	echo "  2) bridge (client)"
	echo "  3) both"
	local c
	read -r -p "Choose [1-3]: " c
	echo -e "${C_YELLOW}(Ctrl+C to stop following logs and return to the menu)${C_RESET}"
	case "$c" in
		1) ( cd "$INSTALL_DIR" && $COMPOSE_CMD logs -f --tail=100 gate ) ;;
		2) ( cd "$INSTALL_DIR" && $COMPOSE_CMD logs -f --tail=100 bridge ) ;;
		*) ( cd "$INSTALL_DIR" && $COMPOSE_CMD logs -f --tail=100 ) ;;
	esac
}

restart_tunnel() {
	require_repo
	log "Restarting running services..."
	( cd "$INSTALL_DIR" && $COMPOSE_CMD restart ) || die "Restart failed"
	ok "Restarted."
}

stop_tunnel() {
	require_repo
	log "Stopping services..."
	( cd "$INSTALL_DIR" && $COMPOSE_CMD down ) || die "Stop failed"
	ok "Stopped."
}

# ---------- menu ----------
print_banner() {
	clear
	echo -e "${C_CYAN}${C_BOLD}"
	echo "  ╔════════════════════════════════════════════╗"
	echo "  ║              tunel — control panel          ║"
	echo "  ╚════════════════════════════════════════════╝"
	echo -e "${C_RESET}"
	echo -e "  Install dir : ${C_BOLD}${INSTALL_DIR}${C_RESET}"
	echo -e "  Compose     : ${C_BOLD}${COMPOSE_CMD}${C_RESET}"
	echo
}

main_menu() {
	while true; do
		print_banner
		echo -e "  ${C_GREEN}[1]${C_RESET} Setup & Start Server (Gate - Iran)"
		echo -e "  ${C_GREEN}[2]${C_RESET} Setup & Start Client (Bridge - Kharej)"
		echo -e "  ${C_GREEN}[3]${C_RESET} View Live Logs"
		echo -e "  ${C_GREEN}[4]${C_RESET} Restart Tunnel"
		echo -e "  ${C_GREEN}[5]${C_RESET} Stop Tunnel"
		echo -e "  ${C_RED}[0]${C_RESET} Exit"
		echo
		local choice
		read -r -p "$(printf '%b' "${C_BOLD}Select an option:${C_RESET} ")" choice
		echo
		case "$choice" in
			1) setup_server && start_server_service ;;
			2) setup_client && start_client_service ;;
			3) view_logs ;;
			4) restart_tunnel ;;
			5) stop_tunnel ;;
			0) echo "Bye."; exit 0 ;;
			*) warn "Invalid option." ;;
		esac
		echo
		read -r -p "Press Enter to return to the menu..." _ || true
	done
}

# ---------- entrypoint ----------
main() {
	require_root
	detect_os
	install_dependencies
	clone_or_update_repo
	detect_compose_cmd
	main_menu
}

main "$@"
