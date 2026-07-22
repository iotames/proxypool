#!/bin/bash
# 测试 --fixed 固定代理模式的脚本
# 测试流程：
#   1. 先启动普通随机模式（不带 --fixed），访问 10 次查看 IP 是否随机变化
#   2. 停止后启动 --fixed 模式，再访问 10 次查看 IP 是否固定
#
# 使用方法：./test_fixed.sh
#
# 注意：测试前确保 proxypool 二进制已编译，且 clash.yaml 在当前目录

set -e

BIN="./proxypool"
PORT=10800
API_PORT=10801
CONF="clash.yaml"
GTIMEOUT=600
IP_SERVICE="https://httpbin.org/ip"
TIMEOUT=15
MAIN_LOG="proxypool_test_output.log"
RANDOM_LOG="proxypool_random.log"
FIXED_LOG="proxypool_fixed.log"
POLL_WAIT=1
POLL_RETRIES=20

# 清空日志文件
> "$MAIN_LOG"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

cleanup() {
    pkill -f "$BIN --fixed --port=$PORT --conf=$CONF" 2>/dev/null || true
    pkill -f "$BIN --port=$PORT --conf=$CONF --GOOGLE_TIMEOUT=$GTIMEOUT" 2>/dev/null || true
}

# 确保退出时清理
trap cleanup EXIT

# 清理上次残留
cleanup

# 等待端口可用的函数
wait_for_port() {
    local port=$1
    local retries=$2
    local log_file=$3
    local attempt=0
    while [ $attempt -lt $retries ]; do
        if nc -z 127.0.0.1 "$port" 2>/dev/null; then
            return 0
        fi
        # 同时也检查日志中是否出现报错
        if [ -n "$log_file" ] && grep -q "没有可用节点" "$log_file" 2>/dev/null; then
            return 2
        fi
        sleep "$POLL_WAIT"
        attempt=$((attempt + 1))
    done
    return 1
}

echo -e "${CYAN}========================================${NC}"
echo -e "${CYAN}  proxypool 固定代理模式测试脚本${NC}"
echo -e "${CYAN}========================================${NC}"
echo ""

# ==============================
# 第一部分：普通随机模式
# ==============================
echo -e "${YELLOW}========== 第一部分：普通随机模式 ==========${NC}"
echo -e "${YELLOW}启动代理（不带 --fixed）...${NC}"

$BIN --port=$PORT --conf=$CONF --GOOGLE_TIMEOUT=$GTIMEOUT > "$RANDOM_LOG" 2>&1 &
RANDOM_PID=$!

# 轮询等待端口就绪
echo -e "等待端口 $PORT 就绪..."
wait_for_port "$PORT" "$POLL_RETRIES" "$RANDOM_LOG"
WAIT_RESULT=$?
if [ "$WAIT_RESULT" -eq 2 ]; then
    echo -e "${RED}没有可用节点，脚本退出${NC}"
    kill $RANDOM_PID 2>/dev/null || true
    exit 1
elif [ "$WAIT_RESULT" -ne 0 ]; then
    echo -e "${RED}代理启动超时（${POLL_RETRIES}秒），脚本退出${NC}"
    kill $RANDOM_PID 2>/dev/null || true
    exit 1
fi
echo -e "${GREEN}端口已就绪${NC}"

echo -e "代理 PID: $RANDOM_PID"
echo -e "访问 ${IP_SERVICE} 共 10 次，记录 IP："
echo ""

declare -a RANDOM_IPS
for i in $(seq 1 10); do
    RESP=$(curl -sS -w "\n%{http_code}" --max-time $TIMEOUT -x http://127.0.0.1:$PORT "$IP_SERVICE" 2>>"$MAIN_LOG") || true
    BODY=$(echo "$RESP" | sed '$d')
    HTTP_CODE=$(echo "$RESP" | tail -1)
    echo "$BODY" | tee -a "$MAIN_LOG"
    echo "HTTP_CODE: $HTTP_CODE" | tee -a "$MAIN_LOG"
    IP=$(echo "$BODY" | grep -o '"origin": "[^"]*"' | cut -d'"' -f4 || echo "请求失败")
    RANDOM_IPS[$i]="$IP"
    echo -e "      状态码: ${CYAN}$HTTP_CODE${NC} | IP: ${GREEN}$IP${NC}"
    [ $i -lt 10 ] && sleep 2
done

# 统计唯一 IP 数
UNIQUE_RANDOM=$(printf '%s\n' "${RANDOM_IPS[@]}" | sort -u | wc -l)
echo ""
echo -e "唯一 IP 数: ${CYAN}$UNIQUE_RANDOM${NC}"
if [ "$UNIQUE_RANDOM" -gt 1 ]; then
    echo -e "${GREEN}✓ 随机模式：IP 有变化，符合预期${NC}"
else
    echo -e "${RED}✗ 随机模式：IP 全部相同，可能节点太少或异常${NC}"
fi
echo ""

# 停止随机模式
echo -e "${YELLOW}停止随机模式代理...${NC}"
kill $RANDOM_PID 2>/dev/null || true
wait $RANDOM_PID 2>/dev/null || true

# ==============================
# 第二部分：固定模式
# ==============================
echo -e "${YELLOW}========== 第二部分：固定代理模式（--fixed） ==========${NC}"
echo -e "${YELLOW}启动代理（带 --fixed）...${NC}"

$BIN --fixed --port=$PORT --conf=$CONF > "$FIXED_LOG" 2>&1 &
FIXED_PID=$!

# 轮询等待端口就绪
echo -e "等待端口 $PORT 就绪..."
wait_for_port "$PORT" "$POLL_RETRIES" "$FIXED_LOG"
WAIT_RESULT=$?
if [ "$WAIT_RESULT" -eq 2 ]; then
    echo -e "${RED}没有可用节点，脚本退出${NC}"
    kill $FIXED_PID 2>/dev/null || true
    exit 1
elif [ "$WAIT_RESULT" -ne 0 ]; then
    echo -e "${RED}代理启动超时（${POLL_RETRIES}秒），脚本退出${NC}"
    kill $FIXED_PID 2>/dev/null || true
    exit 1
fi
echo -e "${GREEN}端口已就绪${NC}"

echo -e "代理 PID: $FIXED_PID"
echo -e "访问 ${IP_SERVICE} 共 5 次，记录 IP："
echo ""

declare -a FIXED_IPS
for i in $(seq 1 5); do
    RESP=$(curl -sS -w "\n%{http_code}" --max-time $TIMEOUT -x http://127.0.0.1:$PORT "$IP_SERVICE" 2>>"$MAIN_LOG") || true
    BODY=$(echo "$RESP" | sed '$d')
    HTTP_CODE=$(echo "$RESP" | tail -1)
    echo "$BODY" | tee -a "$MAIN_LOG"
    echo "HTTP_CODE: $HTTP_CODE" | tee -a "$MAIN_LOG"
    IP=$(echo "$BODY" | grep -o '"origin": "[^"]*"' | cut -d'"' -f4 || echo "请求失败")
    FIXED_IPS[$i]="$IP"
    echo -e "      状态码: ${CYAN}$HTTP_CODE${NC} | IP: ${GREEN}$IP${NC}"
    [ $i -lt 5 ] && sleep 2
done

# 统计
UNIQUE_FIXED=$(printf '%s\n' "${FIXED_IPS[@]}" | sort -u | wc -l)
echo ""
echo -e "唯一 IP 数: ${CYAN}$UNIQUE_FIXED${NC}"
if [ "$UNIQUE_FIXED" -eq 1 ]; then
    echo -e "${GREEN}✓ 固定模式：IP 完全一致，符合预期${NC}"
else
    echo -e "${RED}✗ 固定模式：IP 有变化（期望全部相同）${NC}"
fi

# 停止固定模式
echo ""
echo -e "${YELLOW}停止固定模式代理...${NC}"
kill $FIXED_PID 2>/dev/null || true
wait $FIXED_PID 2>/dev/null || true

# ==============================
# 最终结果汇总
# ==============================
echo ""
echo -e "${CYAN}========================================${NC}"
echo -e "${CYAN}  测试结果汇总${NC}"
echo -e "${CYAN}========================================${NC}"
echo ""
echo -e "随机模式唯一 IP 数: ${CYAN}$UNIQUE_RANDOM${NC}"
echo -e "固定模式唯一 IP 数: ${CYAN}$UNIQUE_FIXED${NC}"

if [ "$UNIQUE_RANDOM" -gt 1 ] && [ "$UNIQUE_FIXED" -eq 1 ]; then
    echo ""
    echo -e "${GREEN}✓ 全部测试通过！${NC}"
elif [ "$UNIQUE_RANDOM" -le 1 ] && [ "$UNIQUE_FIXED" -eq 1 ]; then
    echo ""
    echo -e "${YELLOW}⚠ 随机模式未观察到 IP 变化（可能网络波动或可用节点少），但固定模式工作正常${NC}"
elif [ "$UNIQUE_RANDOM" -gt 1 ] && [ "$UNIQUE_FIXED" -gt 1 ]; then
    echo ""
    echo -e "${RED}✗ 固定模式出现 IP 变化，请检查代码${NC}"
else
    echo ""
    echo -e "${RED}✗ 测试异常，请检查日志${NC}"
fi

echo ""
echo "随机模式日志: $RANDOM_LOG"
echo "固定模式日志: $FIXED_LOG"
echo "curl 详细日志: $MAIN_LOG"
