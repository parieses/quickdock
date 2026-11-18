。**当前是类 Unix 系统，命令由 /bin/sh 执行**：常用 POSIX 工具（head / tail / grep / ls / rm）可用，`$(...)`、管道与重定向都支持。需要 Bash 专有语法（数组、`[[ ]]`、`set -o pipefail`）时显式写 `bash -c "..."`，不要假设默认 shell 就是 bash。
