# Search vocabulary — English

How a task gets described, mapped to what the tool was named. Without this a
query has to already contain the answer: "give someone remote access" describes
the ssh tools exactly and shares not one character with the string "ssh".

One entry per line: `key: term, term, term`. Everything else is prose and is
ignored, so this file can explain itself.

## categories

ssh: remote, sftp, scp, tunnel, port, forward, server, host, login
shell: bash, command, session, console, sh
terminal: console, command, cli, prompt, xterm
browser: chrome, chromium, web, page, url, dom, tab, site
accessibility: a11y, atspi, widget, button, label, element, form, field
windows: window, app, application, focus, raise, geometry
input: keyboard, mouse, click, type, press, key, scroll, drag
screen: display, pixel, ocr, capture, screenshot, resolution, text
files: file, directory, folder, path, read, write, download, upload
processes: process, program, pid, launch, start, run, kill, app
packages: apt, install, package, software, dependency
recording: record, video, capture, mp4, film
restream: rtmp, stream, broadcast, publish, youtube, twitch
room: control, session, participant, viewer, share, turn
audio: sound, volume, mute, speaker, mic
clipboard: copy, paste, cut
desktops: workspace, desktop, virtual
snapshot: backup, restore, checkpoint, rollback
system: service, systemd, sudo, root, privilege, daemon
jobs: job, background, long running, download, unpack, extract, build, install, progress, still going, finished yet, abort, stop it, cancel, watch it

## tools

launch_app: open, start, application, program, app, open the
open_app_and_wait: open and wait, start and wait, launch and wait, until, and wait
run_command: execute, shell, one-off, command line, disk space, free space, how much space, storage, filesystem, how much memory, memory usage, ram, swap, cpu load, load average, temperature, how hot, uptime, kernel version, what distribution, running out of memory, out of space, out of disk, network, ip address, ping, is the network
job_start: in the background, long running, takes a while, download, downloading, fetch a file, wget, curl, unpack, extract, tar, build, compile, start it and, kick off, without waiting, let it run
job_status: still running, still going, is it done, has it finished, did it finish, finished yet, how is it going, progress of the job, did it work, exit code, background task
job_output: printed, what did it print, what the task printed, output of the job, the log, what it said, stdout, stderr, why did it fail, error output, background task
job_wait: wait for it, wait until it finishes, when it is done, once it completes, after the download, block until
job_abort: stop it, abort, cancel it, kill the job, stop that, never mind, wrong one
sleep: wait, pause, sleep, hold on, do nothing for, for three minutes, for 30 seconds, give it a minute, let it run for, while the recording, during the recording, delay, wait a bit, count down
secret_list: password, passwords, credential, credentials, secret, secrets, vault, login, token, api key, passphrase, what password, which credentials, sign in as
activity: what happened, what changed, who did what, history, timeline, while I was away, since I was stopped, what did they do, what did you do, audit, record, log of actions, recent actions
job_list: background work, what jobs, what is running in the background, list jobs, everything running
list_processes: running, tasks, what is running, processes, using the cpu, eating memory, hogging, slowing everything down, using too much, consuming, resource usage, what is slow, cpu, what is eating the cpu, cpu, memory
is_running: already open, is it open, still running, alive
kill_process: stop, terminate, quit, frozen, hung, force quit, not responding, stopped responding, unresponsive, crashed, will not close
list_installed_apps: installed, available applications, what applications
list_commands: binaries, executables, programs, what can i run, path
screenshot: picture, capture, image, look, see
screenshot_region: crop, rectangle, part of the screen, area
get_screen_info: resolution, size, dimensions, how big
get_pixel_color: color, colour, rgb, pixel, dot, shade
read_screen_text: ocr, what does it say, read the screen
find_text: locate, where does it say, where is the word, search the screen
set_resolution: change resolution, resize the display, 1920, 1280
activate_window: front, foreground, bring to front, switch to, raise
get_active_window: which window, has focus, current window, frontmost
move_window: position, corner, place, put the window, reposition
resize_window: narrower, wider, taller, shorter, dimensions
minimize_window: hide, out of the way, iconify, taskbar
maximize_window: as big as, bigger, fill the screen, enlarge
restore_window: unmaximize, back to, previous size, undo maximize
fullscreen_window: full screen, entire display, whole screen
window_properties: details, attributes, geometry, about that window
window_hierarchy: parent, child, tree of windows, nesting
window_set_state: always on top, above the others, sticky, shaded, keep above
wait_for_window: until the window, window to open, window to appear
desktop_state: what is on the screen, where am i, what is going on, current state, everything at once, get my bearings, situation, overview, snapshot, take stock, what is open
list_desktops: workspaces, how many workspaces, virtual desktops
get_desktop_info: which workspace, current workspace, am i on
switch_desktop: go to workspace, next workspace, change workspace
set_window_desktop: send to workspace, move to workspace, another workspace
room_state: who, connected, participants, viewers, people, others, sharing, am i allowed, may i act, can i type, is it my turn, who is driving
ask_human: ask the person, ask them, which did you mean, confirm with, check with the user, prompt
request_control: take the controls, claim, grab, acquire, may i, drive the desktop, let me act, i want to act, take over
release_control: give back, hand back, relinquish, let go of the controls, done
ui_tree: structure, hierarchy, widgets, layout, what is in the app
ui_find: locate the button, search box, which element, find the field
ui_at_point: what is at, under the pointer, at these coordinates, what is there, identify, under the mouse
ui_click: press the button, activate the element, push
ui_focus: cursor, caret, put the cursor, select the field
ui_get_text: read the field, contents of the field, what does it hold
ui_set_text: write into, put text in, enter into the field
ui_diff: changed, difference, since i last, what is new
ui_wait_for: until it appears, dialog to appear, element to appear
fill_form: complete the form, several fields, fill in
terminal_open: terminal window, xterm, console window
terminal_run: into the terminal, at the prompt, in the console
terminal_read: terminal output, what the terminal, console output
shell_open: background session, persistent, keep using, long running
shell_exec: in the session, same session, persistent command
shell_input: send a line, answer the prompt, stdin, waiting for input
shell_read: session output, what the session, printed
shell_list: open sessions, my sessions, which sessions
shell_close: end the session, finish the session
check_errors: fail, failed, failure, problem, wrong, broken, crash, went wrong
browser_open: website, web site, web page, url
browser_goto: navigate, different address, another page, go to
browser_text: page contents, what the page says, read the page
browser_type: text box, input field, into the website, on the site
browser_click: press on the page, button on the page, link
browser_eval: javascript, js, script in the page, evaluate
browser_tabs: open pages, which tabs, what is open in the browser
browser_wait_for: until the element, element to appear, page to show
read_file: contents of the file, cat, show the file
write_file: save to a file, create a file, put in a file
list_directory: folder, what is inside, ls, contents of the directory
install_packages: apt install, add software, get the package
remove_packages: uninstall, purge, get rid of the package, delete the package
search_packages: is there a package, look for software, find a package
snapshot_create: checkpoint, save the state, come back to, backup
snapshot_list: checkpoints, what backups, saved states
snapshot_restore: roll back, revert, go back to, undo everything
snapshot_delete: throw away the checkpoint, remove the backup
start_recording: film, capture video, make a video, record
stop_recording: stop the video, finish recording, end the recording
get_recording_status: still recording, am i recording, is it recording
list_recordings: videos, what have i recorded, past recordings
start_restream: youtube, twitch, go live, broadcast
stop_restream: stop the broadcast, go offline, end the stream
list_restreams: broadcasts, what is live, active streams
ssh_connect: log in to, sign in to, open a connection, reach the machine
ssh_disconnect: close the connection, log out, drop the connection
ssh_list: which hosts, my connections, connected to
ssh_exec: on the remote, on that machine, over ssh
ssh_upload: send the file, copy to the server, put the file
ssh_download: fetch the file, copy from the server, get the file
ssh_list_remote: files on the remote, directory on the server, what is on the server
ssh_keygen: key pair, private key, public key, make a key
ssh_copy_id: passwordless, install the key, trust the key, without a password
ssh_tunnel_local: port forward, forward a local port, reach a remote service
ssh_tunnel_remote: reverse tunnel, expose locally, publish my service
ssh_tunnels: open forwards, which tunnels, active tunnels, forwards
ssh_tunnel_close: close the tunnel, shut down, stop forwarding, tear down
remote_open: remote desktop, rdp, vnc, spice, connect to a windows machine, connect to a pc, remote screen, view another computer, remmina, freerdp
remote_close: disconnect the remote desktop, close the remote session, end the rdp session, hang up the vnc
remote_list: which remote desktops are open, open remote sessions, active remote desktops, remote sessions right now, currently connected remote
remote_profile_save: save the remote connection, remember this rdp host, store a vnc profile, bookmark the remote machine
remote_profile_list: saved remote desktop profiles, list saved remote profiles, my rdp profiles, stored remote connections
remote_profile_delete: delete a saved remote desktop profile, forget the remote profile, remove the rdp profile
mouse_click: click at, coordinates, click there
mouse_move: pointer to, move the cursor, hover
mouse_down: hold the button, press and hold, begin the drag
mouse_up: let go, release the button, end the drag
mouse_drag: drag and drop, drag onto, move it onto
mouse_scroll: wheel, scroll down, scroll up
get_mouse_position: where is the pointer, cursor position, pointer location
type_text: write, enter text, keyboard
key_combo: shortcut, control and, press ctrl, hotkey, modifier
get_clipboard: what did i copy, paste buffer, copied
set_clipboard: copy this, put on the clipboard, make it pasteable
get_audio_state: muted, how loud, is there sound
set_volume: louder, quieter, turn the sound, turn it down, turn it up
sudo_status: as root, privileges, am i allowed, elevated
service_control: restart the, daemon, supervisor, bounce the
action_log: history, audit, what has been done, trail, past calls
subscribe_events: notify, tell me when, instead of polling, be told, watch for changes, let me know
unsubscribe_events: stop notifying, no more notifications, stop telling me, stop sending
wait: pause, sleep, delay, for a moment, seconds
wait_for_idle: stops changing, settles, quiet, finishes drawing, stable
