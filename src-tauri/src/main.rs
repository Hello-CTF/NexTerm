// NexTerm 桌面入口：仅启动，所有装配在 lib.rs
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    nexterm_lib::run();
}
