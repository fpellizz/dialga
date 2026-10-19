// Dialga desktop shell: starts the Go engine as a sidecar, hands it a
// per-session token on stdin and tells the frontend where to reach it.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::sync::{Condvar, Mutex};
use std::time::Duration;

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Manager, RunEvent, State};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};

/// What the frontend needs to call the engine API.
#[derive(Clone, Serialize)]
struct EngineInfo {
    url: String,
    token: String,
    version: String,
}

#[derive(Default)]
struct Engine {
    info: Mutex<Option<EngineInfo>>,
    error: Mutex<Option<String>>,
    child: Mutex<Option<CommandChild>>,
    /// True once the engine process has exited.
    exited: (Mutex<bool>, Condvar),
}

/// How long the engine has to stop by itself (closing its SSM tunnels with
/// TerminateSession) before it is killed.
const STOP_GRACE: Duration = Duration::from_secs(10);

/// First stdout line of "dialga serve".
#[derive(Deserialize)]
struct ReadyLine {
    event: String,
    port: u16,
    version: String,
}

/// Where the frontend reaches the engine; an error while the engine starts
/// or after it died.
#[tauri::command]
fn engine_info(engine: State<'_, Engine>) -> Result<EngineInfo, String> {
    if let Some(info) = engine.info.lock().unwrap().clone() {
        return Ok(info);
    }
    Err(engine
        .error
        .lock()
        .unwrap()
        .clone()
        .unwrap_or_else(|| "engine is starting".into()))
}

fn session_token() -> Result<String, String> {
    let mut bytes = [0u8; 32];
    getrandom::fill(&mut bytes).map_err(|e| e.to_string())?;
    Ok(bytes.iter().map(|b| format!("{b:02x}")).collect())
}

fn start_engine(app: &AppHandle) -> Result<(), Box<dyn std::error::Error>> {
    let token = session_token()?;
    let (mut events, mut child) = app.shell().sidecar("dialga")?.args(["serve"]).spawn()?;
    // The token goes on stdin, never on the command line (ps would show it).
    // The engine also uses stdin as a watchdog: it exits when we close it or
    // write "shutdown" (stop_engine).
    child.write(format!("{token}\n").as_bytes())?;
    app.state::<Engine>().child.lock().unwrap().replace(child);

    let handle = app.clone();
    tauri::async_runtime::spawn(async move {
        while let Some(event) = events.recv().await {
            let engine = handle.state::<Engine>();
            match event {
                CommandEvent::Stdout(line) => {
                    if let Ok(ready) = serde_json::from_slice::<ReadyLine>(&line) {
                        if ready.event == "ready" {
                            engine.info.lock().unwrap().replace(EngineInfo {
                                url: format!("http://127.0.0.1:{}", ready.port),
                                token: token.clone(),
                                version: ready.version,
                            });
                        }
                    }
                }
                CommandEvent::Stderr(line) => {
                    eprintln!("engine: {}", String::from_utf8_lossy(&line).trim_end());
                }
                CommandEvent::Error(error) => {
                    engine.error.lock().unwrap().replace(error);
                }
                CommandEvent::Terminated(status) => {
                    engine.info.lock().unwrap().take();
                    engine
                        .error
                        .lock()
                        .unwrap()
                        .replace(format!("engine exited (code {:?})", status.code));
                    let (exited, changed) = &engine.exited;
                    *exited.lock().unwrap() = true;
                    changed.notify_all();
                }
                _ => {}
            }
        }
    });
    Ok(())
}

fn main() {
    // GTK takes the Wayland app_id (and the X11 WM_CLASS) from the program
    // name, which is the binary name, dialga-desktop. KDE and GNOME look for
    // <app_id>.desktop to give the window its icon: "Dialga" matches the
    // Dialga.desktop of the packages, instead of a generic icon.
    #[cfg(target_os = "linux")]
    glib::set_prgname(Some("Dialga"));

    let app = tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .manage(Engine::default())
        .invoke_handler(tauri::generate_handler![engine_info])
        .setup(|app| {
            start_engine(app.handle())?;
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while building the Dialga shell");

    app.run(|handle, event| {
        if let RunEvent::Exit = event {
            stop_engine(handle);
        }
    });
}

/// Asks the engine to stop with a "shutdown" line on stdin, so it closes its
/// SSM tunnels, and waits STOP_GRACE for it; kills it only if it is still
/// there. A killed engine leaves its SSM sessions open until they time out.
fn stop_engine(handle: &AppHandle) {
    let engine = handle.state::<Engine>();
    let Some(mut child) = engine.child.lock().unwrap().take() else {
        return;
    };
    if child.write(b"shutdown\n").is_ok() {
        let (exited, changed) = &engine.exited;
        let (exited, _) = changed
            .wait_timeout_while(exited.lock().unwrap(), STOP_GRACE, |exited| !*exited)
            .unwrap();
        if *exited {
            return;
        }
    }
    let _ = child.kill();
}
