# Description

基于 GoLang 的精简版 TCP 测速程序，原地址 [https://github.com/jspiegler/goperf](https://github.com/jspiegler/goperf)

# Installation

<tt>go get github.com/lzxz1234/iperf/iperf</tt>

# Usage

命令行直接调用 *goperf*，可选参数如下：

| Flag       | Parameter Type | Description                                                                             |
|------------|----------------|-----------------------------------------------------------------------------------------|
| -Mbps      | integer        | -Mbps nnn: for UDP connections, transmit at *Mbps* (M=1000000)                          |
| -c         | string         | -c host:port: run as client, making connection to IP address *host*, port number *port* |
| -nb        | integer        | -nb nnn: send/receive *nnn* bytes, then quit (default: no byte limit)                   |
| -ns        | integer        | -ns nnn: send/receive for *nnn* seconds, then quit (default: no time limit)             |
| -rate      | string         | -rate nnn[X]: transmit at *nnn* bps, with an optional multiplier *X* (K, M, or G)           |
| -s         | string         | -s N, for server operation, listen on port *N* (all interfaces)                         |
| -scroll    | boolean        | -scroll: make output scroll (default: no scroll)                                        |
| -ts        | boolean        | -ts: display timestamp on each line of output                                           |
| -v         | boolean        | -v: display version and quit                                                            |

# 举例

服务端执行 `iperf -s 8800` <br />
客户端执行 `iperf -c 127.0.0.1:8800`