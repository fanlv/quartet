#!/bin/bash

set -u

ROOT_PID="${1:-}"

if [ -z "$ROOT_PID" ]; then
    exit 0
fi

all_pids="$ROOT_PID"
queue="$ROOT_PID"

while [ -n "$queue" ]; do
    next_queue=""
    for pid in $queue; do
        children=$(pgrep -P "$pid" 2>/dev/null || true)
        if [ -n "$children" ]; then
            all_pids="$all_pids $children"
            next_queue="$next_queue $children"
        fi
    done
    queue="$next_queue"
done

kill $all_pids 2>/dev/null || true
sleep 2

for pid in $all_pids; do
    if kill -0 "$pid" 2>/dev/null; then
        kill -9 "$pid" 2>/dev/null || true
    fi
done
