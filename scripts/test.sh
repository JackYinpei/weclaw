#!/bin/zsh
# =============================================================================
# WeClaw 功能测试脚本
# =============================================================================
# 测试内容:
#   1. 健康检查
#   2. Docker 连通性测试 (创建测试容器并验证)
#   3. 用户注册 (模拟微信关注，创建用户+容器)
#   4. 发送消息 (向用户的 OpenClaw 容器发送消息)
#   5. 查询用户信息
#   6. 清理测试数据
#
# 用法:
#   chmod +x scripts/test.sh
#   ./scripts/test.sh                    # 运行全部测试
#   ./scripts/test.sh health             # 只运行健康检查
#   ./scripts/test.sh docker             # 只测试 Docker
#   ./scripts/test.sh register           # 只测试注册
#   ./scripts/test.sh send               # 只测试发消息
#   ./scripts/test.sh cleanup            # 清理测试用户
#   ./scripts/test.sh full               # 完整流程测试 (注册→发消息→清理)
# =============================================================================

set -euo pipefail

# --- 配置 ---
BASE_URL="${WECLAW_URL:-http://localhost:8080}"
TEST_OPENID="${TEST_OPENID:-test-user-$(date +%s)}"
TEST_MESSAGE="${TEST_MESSAGE:-你好，请介绍一下你自己}"

# --- 颜色 ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color
BOLD='\033[1m'

# --- 辅助函数 ---
print_header() {
    echo ""
    echo "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo "${BOLD}${CYAN}  $1${NC}"
    echo "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
}

print_step() {
    echo ""
    echo "${YELLOW}▶ $1${NC}"
}

print_ok() {
    echo "${GREEN}  ✅ $1${NC}"
}

print_fail() {
    echo "${RED}  ❌ $1${NC}"
}

print_info() {
    echo "${CYAN}  ℹ️  $1${NC}"
}

print_json() {
    if command -v jq &> /dev/null; then
        echo "$1" | jq '.'
    else
        echo "$1"
    fi
}

# 检查服务是否运行
check_server() {
    if ! curl -s --connect-timeout 3 "${BASE_URL}/healthz" > /dev/null 2>&1; then
        echo ""
        print_fail "WeClaw 服务未运行! 请先启动服务:"
        echo "    ${BOLD}go run cmd/weclaw/main.go${NC}"
        echo ""
        exit 1
    fi
}

# --- 测试函数 ---

test_health() {
    print_header "🏥 健康检查"
    print_step "GET ${BASE_URL}/healthz"

    response=$(curl -s -w "\n%{http_code}" "${BASE_URL}/healthz")
    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "200" ]; then
        print_ok "服务正常运行 (HTTP $http_code)"
        print_json "$body"
        return 0
    else
        print_fail "服务异常 (HTTP $http_code)"
        echo "$body"
        return 1
    fi
}

test_docker() {
    print_header "🐳 Docker 连通性测试"
    print_step "GET ${BASE_URL}/api/test/docker"
    print_info "这会创建一个测试容器，验证后自动清理 (可能需要30-60秒)..."

    response=$(curl -s -w "\n%{http_code}" --max-time 120 "${BASE_URL}/api/test/docker")
    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "200" ]; then
        resp_status=$(echo "$body" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
        if [ "$resp_status" = "OK" ]; then
            print_ok "Docker 测试通过!"
        else
            print_fail "Docker 测试失败"
        fi

        # 解析关键字段
        container_created=$(echo "$body" | grep -o '"container_created":[a-z]*' | cut -d: -f2)
        container_running=$(echo "$body" | grep -o '"container_running":[a-z]*' | cut -d: -f2)
        openclaw_ready=$(echo "$body" | grep -o '"openclaw_ready":[a-z]*' | cut -d: -f2)

        echo ""
        [ "$container_created" = "true" ] && print_ok "容器创建: 成功" || print_fail "容器创建: 失败"
        [ "$container_running" = "true" ] && print_ok "容器运行: 成功" || print_fail "容器运行: 失败"
        [ "$openclaw_ready" = "true" ] && print_ok "OpenClaw 响应: 成功" || print_info "OpenClaw 响应: 未就绪 (可能需要更多启动时间)"

        echo ""
        print_info "完整响应:"
        print_json "$body"
        return 0
    else
        print_fail "请求失败 (HTTP $http_code)"
        echo "$body"
        return 1
    fi
}

test_register() {
    local openid="${1:-$TEST_OPENID}"

    print_header "📝 用户注册测试"
    print_step "POST ${BASE_URL}/api/test/register"
    print_info "OpenID: $openid"
    print_info "这会创建用户并启动 OpenClaw 容器 (可能需要30-120秒)..."

    response=$(curl -s -w "\n%{http_code}" --max-time 180 \
        -X POST \
        -H "Content-Type: application/json" \
        -d "{\"openid\": \"$openid\"}" \
        "${BASE_URL}/api/test/register")
    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "200" ]; then
        resp_status=$(echo "$body" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
        if [ "$resp_status" = "OK" ]; then
            print_ok "用户注册成功!"
        elif [ "$resp_status" = "already_exists" ]; then
            print_info "用户已存在"
        else
            print_fail "注册状态: $resp_status"
        fi

        echo ""
        print_info "完整响应:"
        print_json "$body"
        return 0
    else
        print_fail "注册失败 (HTTP $http_code)"
        echo "$body"
        return 1
    fi
}

test_send_message() {
    local openid="${1:-$TEST_OPENID}"
    local message="${2:-$TEST_MESSAGE}"

    print_header "💬 消息发送测试"
    print_step "POST ${BASE_URL}/api/test/send"
    print_info "OpenID: $openid"
    print_info "消息: $message"
    print_info "等待 OpenClaw 处理 (可能需要10-60秒)..."

    response=$(curl -s -w "\n%{http_code}" --max-time 180 \
        -X POST \
        -H "Content-Type: application/json" \
        -d "{\"openid\": \"$openid\", \"message\": \"$message\"}" \
        "${BASE_URL}/api/test/send")
    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "200" ]; then
        resp_status=$(echo "$body" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
        if [ "$resp_status" = "OK" ]; then
            print_ok "消息发送成功!"

            # Try to extract and show response
            if command -v jq &> /dev/null; then
                echo ""
                echo "${CYAN}  📤 原始响应:${NC}"
                echo "$body" | jq -r '.raw_response // "N/A"' | head -20
                echo ""
                echo "${CYAN}  📱 微信格式:${NC}"
                echo "$body" | jq -r '.wechat_formatted // "N/A"' | head -20
                echo ""
                echo "${CYAN}  ⏱️  耗时: $(echo "$body" | jq -r '.elapsed // "N/A"')${NC}"
            fi
        else
            print_fail "发送失败: $resp_status"
        fi

        echo ""
        print_info "完整响应:"
        print_json "$body"
        return 0
    else
        print_fail "请求失败 (HTTP $http_code)"
        echo "$body"
        return 1
    fi
}

test_get_user() {
    local openid="${1:-$TEST_OPENID}"

    print_header "👤 查询用户信息"
    print_step "GET ${BASE_URL}/api/test/user/$openid"

    response=$(curl -s -w "\n%{http_code}" "${BASE_URL}/api/test/user/$openid")
    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "200" ]; then
        print_ok "用户信息获取成功"
        print_json "$body"
        return 0
    elif [ "$http_code" = "404" ]; then
        print_info "用户不存在: $openid"
        return 1
    else
        print_fail "请求失败 (HTTP $http_code)"
        echo "$body"
        return 1
    fi
}

test_list_users() {
    print_header "📋 列出所有用户"
    print_step "GET ${BASE_URL}/api/test/users"

    response=$(curl -s -w "\n%{http_code}" "${BASE_URL}/api/test/users")
    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "200" ]; then
        if command -v jq &> /dev/null; then
            count=$(echo "$body" | jq '.count')
            print_ok "共 $count 个用户"
        else
            print_ok "获取成功"
        fi
        print_json "$body"
        return 0
    else
        print_fail "请求失败 (HTTP $http_code)"
        echo "$body"
        return 1
    fi
}

test_cleanup() {
    local openid="${1:-$TEST_OPENID}"

    print_header "🧹 清理测试数据"
    print_step "DELETE ${BASE_URL}/api/test/user/$openid"
    print_info "删除用户及其容器: $openid"

    response=$(curl -s -w "\n%{http_code}" -X DELETE "${BASE_URL}/api/test/user/$openid")
    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "200" ]; then
        print_ok "清理完成"
        print_json "$body"
        return 0
    elif [ "$http_code" = "404" ]; then
        print_info "用户不存在，无需清理"
        return 0
    else
        print_fail "清理失败 (HTTP $http_code)"
        echo "$body"
        return 1
    fi
}

test_wechat_verify() {
    print_header "🔐 微信签名验证测试"
    print_step "GET ${BASE_URL}/wechat (模拟微信服务器验证)"

    # 模拟微信的验证请求
    # 需要正确计算签名
    timestamp="1677000000"
    nonce="test_nonce"
    echostr="echo_test_string"

    # 读取配置的 token (默认 haojiahuo)
    token="${WECHAT_TOKEN:-haojiahuo}"

    # 计算 sha1(sort(token, timestamp, nonce))
    signature=$(echo -n "$(echo -e "${nonce}\n${timestamp}\n${token}" | sort | tr -d '\n')" | shasum -a 1 | cut -d' ' -f1)

    url="${BASE_URL}/wechat?signature=${signature}&timestamp=${timestamp}&nonce=${nonce}&echostr=${echostr}"
    print_info "Token: $token"
    print_info "Signature: $signature"

    response=$(curl -s -w "\n%{http_code}" "$url")
    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "200" ] && [ "$body" = "$echostr" ]; then
        print_ok "微信签名验证通过! (返回 echostr: $body)"
        return 0
    else
        print_fail "验证失败 (HTTP $http_code, body: $body)"
        return 1
    fi
}

# --- 完整流程测试 ---

test_full() {
    local openid="test-full-$(date +%s)"

    print_header "🚀 完整流程测试"
    print_info "使用临时 OpenID: $openid"

    echo ""
    echo "${YELLOW}Step 1/5: 健康检查${NC}"
    test_health || { print_fail "健康检查失败，终止测试"; return 1; }

    echo ""
    echo "${YELLOW}Step 2/5: 微信签名验证${NC}"
    test_wechat_verify || { print_fail "签名验证失败，继续测试..."; }

    echo ""
    echo "${YELLOW}Step 3/5: 用户注册 (创建容器)${NC}"
    test_register "$openid" || { print_fail "注册失败，终止测试"; return 1; }

    echo ""
    echo "${YELLOW}Step 4/5: 查询用户信息${NC}"
    test_get_user "$openid" || { print_info "查询失败，继续测试..."; }

    echo ""
    echo "${YELLOW}Step 5/5: 发送消息${NC}"
    # 等一会让容器完全就绪（Gateway 启动需要约 30 秒）
    print_info "等待容器完全启动 (40秒)..."
    sleep 40
    test_send_message "$openid" "说'测试通过'这三个字" || { print_info "消息发送失败，可能 OpenClaw 还未就绪"; }

    echo ""
    echo "${YELLOW}清理: 删除测试用户和容器${NC}"
    test_cleanup "$openid"

    print_header "✅ 完整流程测试结束"
}

# --- 主逻辑 ---

echo ""
echo "${BOLD}${BLUE}╔═══════════════════════════════════════════════╗${NC}"
echo "${BOLD}${BLUE}║         WeClaw 功能测试脚本 v1.0              ║${NC}"
echo "${BOLD}${BLUE}╚═══════════════════════════════════════════════╝${NC}"
echo ""
echo "  服务地址: ${BOLD}${BASE_URL}${NC}"
echo "  测试 OpenID: ${BOLD}${TEST_OPENID}${NC}"
echo ""

# 检查依赖
if ! command -v curl &> /dev/null; then
    print_fail "需要安装 curl"
    exit 1
fi

if ! command -v jq &> /dev/null; then
    print_info "建议安装 jq 以获得更好的 JSON 格式化输出: brew install jq"
fi

# 检查服务是否运行
check_server

# 根据参数运行测试
case "${1:-all}" in
    health)
        test_health
        ;;
    docker)
        test_docker
        ;;
    register)
        test_register "${2:-$TEST_OPENID}"
        ;;
    send)
        test_send_message "${2:-$TEST_OPENID}" "${3:-$TEST_MESSAGE}"
        ;;
    user)
        test_get_user "${2:-$TEST_OPENID}"
        ;;
    users)
        test_list_users
        ;;
    verify)
        test_wechat_verify
        ;;
    cleanup)
        test_cleanup "${2:-$TEST_OPENID}"
        ;;
    full)
        test_full
        ;;
    all)
        test_health
        test_wechat_verify
        test_list_users
        print_header "💡 更多测试"
        echo ""
        echo "  运行完整流程测试 (包含 Docker 容器创建):"
        echo "    ${BOLD}./scripts/test.sh full${NC}"
        echo ""
        echo "  单独测试 Docker 连通性:"
        echo "    ${BOLD}./scripts/test.sh docker${NC}"
        echo ""
        echo "  注册测试用户:"
        echo "    ${BOLD}./scripts/test.sh register my-test-user${NC}"
        echo ""
        echo "  发送消息:"
        echo "    ${BOLD}./scripts/test.sh send my-test-user '你好'${NC}"
        echo ""
        echo "  查看用户信息:"
        echo "    ${BOLD}./scripts/test.sh user my-test-user${NC}"
        echo ""
        echo "  清理测试用户:"
        echo "    ${BOLD}./scripts/test.sh cleanup my-test-user${NC}"
        ;;
    *)
        echo "用法: $0 {health|docker|register|send|user|users|verify|cleanup|full|all}"
        echo ""
        echo "命令说明:"
        echo "  health    - 健康检查"
        echo "  docker    - Docker 连通性测试 (创建临时容器)"
        echo "  register  - 注册测试用户 [openid]"
        echo "  send      - 发送消息 [openid] [message]"
        echo "  user      - 查看用户信息 [openid]"
        echo "  users     - 列出所有用户"
        echo "  verify    - 微信签名验证测试"
        echo "  cleanup   - 删除测试用户 [openid]"
        echo "  full      - 完整流程测试 (注册→发消息→清理)"
        echo "  all       - 运行基础测试 (默认)"
        exit 1
        ;;
esac

echo ""
