# Search vocabulary — Spanish

Terms are MERGED with every other file here, not substituted for them. A query
is matched against every word anybody has written for a tool, in any language,
because the searcher has no idea which language it is being asked in and does
not need one.

This file exists because the gap had a price. Measured against the same
catalogue:

    "close that window"            15 tools
    "cerrá esa ventana"             0 tools   → the whole catalogue is offered
    "open a terminal and run..."   15 tools
    "abrí una terminal y corré..."  4 tools

Zero matches is not a small miss. It falls back to offering all 121 schemas,
which is roughly five times the input tokens of a run that matched — so a person
asking in their own language paid five times over, for nothing they did wrong.

Deliberately not a translation of en.md. Translating "one-off" or "frontmost"
produces words nobody types. What is here is what somebody actually says when
they want the thing: the verbs in the forms they arrive in, including the
Rioplatense imperatives (`cerrá`, `abrí`, `sacá`) that a dictionary would miss,
and stems short enough to survive conjugation — `cerr`, `abr`, `mov` match
`cerrar`, `cerrá`, `cerrame` without one entry each.

One entry per line: `key: term, term, term`. Everything else is prose.

## categories

ssh: remoto, remota, sftp, servidor, anfitrión, túnel, tunel, puerto, reenvío, reenvio, acceso remoto, conectarme
shell: consola, comando, orden, sesión, sesion, intérprete, interprete
terminal: terminal, consola, comando, línea de comandos, linea de comandos, prompt
browser: navegador, chrome, chromium, web, página, pagina, url, pestaña, pestana, sitio, dom
accessibility: accesibilidad, widget, botón, boton, etiqueta, elemento, formulario, campo, control
windows: ventana, ventanas, foco, enfocar, geometría, geometria, aplicación, aplicacion
input: teclado, mouse, ratón, raton, clic, click, hacer clic, tipear, escribir, tecla, presionar, arrastrar, desplazar
screen: pantalla, monitor, píxel, pixel, captura, resolución, resolucion, texto en pantalla
files: archivo, archivos, fichero, carpeta, directorio, ruta, leer, escribir, descargar, subir
processes: proceso, procesos, programa, pid, lanzar, arrancar, iniciar, correr, ejecutar, matar, cerrar
packages: paquete, instalar, software, dependencia, apt
audio: sonido, audio, volumen, silenciar, mudo, altavoz, parlante
clipboard: portapapeles, copiar, pegar, copiado
desktops: escritorio, escritorios, espacio de trabajo, workspace
recording: grabar, grabación, grabacion, video, filmar
restream: retransmitir, transmitir, streaming, emitir
snapshot: punto de restauración, punto de restauracion, respaldo, copia, instantánea, instantanea
room: sala, quién, quien, conectado, conectados, participantes, personas, compartiendo, control, permiso
system: sistema, servicio, sudo, permisos, privilegios
jobs: tarea, tareas, trabajo, segundo plano, fondo, descarga, descargar, descomprimir, avance, progreso, sigue, terminó, termino, abortar, frenar, parar

## tools

launch_app: abrir, abrí, abri, abre, lanzar, lanzá, iniciar, arrancar, ejecutar la aplicación, programa
open_app_and_wait: abrir y esperar, lanzar y esperar, hasta que abra
run_command: ejecutar, ejecutá, correr, corré, corre, comando, espacio en disco, espacio libre, cuánto espacio, cuanto espacio, almacenamiento, memoria, cuánta memoria, cuanta memoria, ram, carga, temperatura, tiempo encendido, red, dirección ip, direccion ip, ping
list_processes: procesos, qué está corriendo, que esta corriendo, qué se está ejecutando, consumiendo, usando el procesador, cpu, memoria, lento, anda lento
kill_process: matar, matá, cerrar, cerrá, terminar, forzar, colgado, congelado, no responde, trabado
is_running: está corriendo, esta corriendo, está abierto, esta abierto, sigue abierto
list_installed_apps: instalado, instaladas, qué aplicaciones, que aplicaciones, aplicaciones disponibles
list_commands: binarios, ejecutables, qué puedo correr, que puedo correr

screenshot: captura, capturá, capturar, sacá una foto, saca una foto, pantallazo, foto de la pantalla, ver la pantalla, mirá la pantalla, mira la pantalla
screenshot_region: recorte, recortar, una parte de la pantalla, región, region, área, area
read_screen_text: leer la pantalla, qué dice la pantalla, que dice la pantalla, texto en pantalla, ocr
find_text: dónde dice, donde dice, buscar en pantalla, encontrar el texto
get_pixel_color: color, qué color, que color, píxel, pixel, tono
get_screen_info: resolución, resolucion, tamaño de la pantalla, tamano de la pantalla
set_resolution: cambiar resolución, cambiar resolucion, cambiar el tamaño

list_windows: ventanas, qué ventanas, que ventanas, cuántas ventanas, cuantas ventanas, qué hay abierto, que hay abierto
get_active_window: cuál ventana, cual ventana, ventana activa, tiene el foco, en primer plano
activate_window: traer al frente, enfocar, poner adelante, cambiar a
move_window: mover, mové, move, correr la ventana, posición, posicion, esquina
resize_window: redimensionar, cambiar el tamaño, cambiar el tamano, más grande, mas grande, más chica, mas chica
close_window: cerrar, cerrá, cerra, cierra, cerrame, quitar la ventana, sacar la ventana
minimize_window: minimizar, minimizá, achicar, mandar abajo, ocultar
maximize_window: maximizar, maximizá, agrandar, pantalla completa, ocupar todo
restore_window: restaurar, volver al tamaño, volver al tamano, desmaximizar
fullscreen_window: pantalla completa, pantalla entera
window_set_state: siempre visible, arriba de todo, encima, fijar
set_window_desktop: mandar a otro escritorio, mover al escritorio
switch_desktop: cambiar de escritorio, ir al escritorio, otro escritorio
list_desktops: escritorios, cuántos escritorios, cuantos escritorios, espacios de trabajo
desktop_state: cómo está todo, como esta todo, estado del escritorio, qué está pasando, que esta pasando, dónde estoy, donde estoy, panorama, situación, situacion
get_desktop_info: información del sistema, informacion del sistema, en qué escritorio, en que escritorio

terminal_open: abrir una terminal, abrí una terminal, abri una terminal, nueva terminal
terminal_run: en la terminal, en la consola, escribir en la terminal, tipear en la terminal
terminal_read: leer la terminal, qué dice la terminal, que dice la terminal, la salida
check_errors: error, errores, falló, fallo, salió mal, salio mal, cartel de error, mensaje de error

remote_open: escritorio remoto, rdp, vnc, spice, conectarme a una máquina windows, conectar a una pc, ver otra computadora, remmina, freerdp
remote_close: desconectar el escritorio remoto, cerrar la sesión remota, terminar la sesión rdp, cortar el vnc
remote_list: qué escritorios remotos, sesiones remotas abiertas, máquinas remotas conectadas
remote_profile_save: guardar la conexión remota, recordar este host rdp, guardar un perfil vnc
remote_profile_list: conexiones remotas guardadas, mis perfiles rdp, escritorios remotos guardados
remote_profile_delete: olvidar la conexión remota, borrar el perfil rdp, eliminar un escritorio remoto guardado

mouse_move: mover el mouse, mover el ratón, mover el raton, llevar el puntero
mouse_click: clic, click, hacer clic, apretar, tocar, presionar el botón, presionar el boton
mouse_drag: arrastrar, arrastrá, soltar, mover arrastrando
mouse_scroll: desplazar, scrollear, bajar, subir, rueda
type_text: escribir, escribí, escribi, tipear, tipeá, poner el texto, ingresar
key_combo: atajo, combinación, combinacion, control c, teclas juntas, presionar teclas

ui_find: encontrar el botón, encontrar el boton, buscar el campo, dónde está el, donde esta el
ui_click: hacer clic en, apretar el botón, apretar el boton, tocar el
ui_set_text: llenar el campo, completar, poner en el campo
ui_get_text: qué dice el campo, que dice el campo, leer el campo
ui_tree: estructura, árbol, arbol, qué elementos, que elementos
fill_form: llenar el formulario, completar el formulario

browser_open: abrir el navegador, abrí el navegador, abrir chrome
browser_goto: ir a, navegar a, entrar a, abrir la página, abrir la pagina
browser_text: leer la página, leer la pagina, qué dice la página, que dice la pagina
browser_click: clic en la página, clic en la pagina, apretar en el sitio
browser_type: escribir en la página, escribir en la pagina, llenar en el sitio

read_file: leer el archivo, ver el archivo, mostrame el archivo, contenido del archivo
write_file: escribir el archivo, guardar en un archivo, crear el archivo
list_directory: listar, qué hay en la carpeta, que hay en la carpeta, contenido del directorio

get_audio_state: volumen, está silenciado, esta silenciado, sonido, cómo está el audio, como esta el audio
set_volume: subir el volumen, bajar el volumen, silenciar, poner el volumen, mudo

get_clipboard: qué copié, que copie, portapapeles, lo que está copiado, lo que esta copiado
set_clipboard: copiar, poner en el portapapeles, dejar copiado

start_recording: grabar, empezá a grabar, empeza a grabar, grabá la pantalla, graba la pantalla
stop_recording: parar la grabación, parar la grabacion, terminar de grabar, dejar de grabar

room_state: quién está, quien esta, quién hay, quien hay, hay alguien, participantes, puedo actuar, me dejan, es mi turno
request_control: tomar el control, pedir el control, dame el control, quiero manejar
release_control: soltar el control, devolver el control, liberar, ya terminé, ya termine
ask_human: preguntale, preguntá, pregunta a la persona, consultá, confirmá con

install_packages: instalar, instalá, instala, agregar el paquete
service_control: servicio, arrancar el servicio, parar el servicio, reiniciar el servicio
sudo_status: permisos, tengo sudo, soy root, privilegios

job_start: en segundo plano, en el fondo, descargar, descargá, descarga, bajar el archivo, wget, curl, descomprimir, descomprimí, extraer, compilar, tarda, va a tardar, dejalo corriendo, sin esperar
job_status: sigue corriendo, ya terminó, ya termino, terminó la tarea, termino la tarea, cómo va, como va, avance, código de salida, codigo de salida
job_output: qué imprimió, que imprimio, salida de la tarea, el log, qué dijo, que dijo, por qué falló, por que fallo, salida de error
job_wait: esperar a que termine, esperá que termine, cuando termine, una vez que termine, después de la descarga, despues de la descarga
job_abort: frenar, frená, abortar, abortá, parar, pará, cancelar, cancelá, matar la tarea, no era eso
sleep: esperar, esperá, espera, pausa, pausar, poner en pausa, dormir, hacer nada por, por tres minutos, por 30 segundos, dale un minuto, mientras graba, durante la grabación, durante la grabacion, demora, aguantá, cuenta regresiva
secret_list: contraseña, contrasena, contraseñas, clave, claves, credencial, credenciales, secreto, secretos, baulera, bóveda, boveda, token, api key, usuario y clave, qué contraseña, que contrasena
type_secret: escribí la contraseña, escribi la contrasena, poné la clave, pone la clave, ingresá la contraseña, tipear la contraseña, campo de contraseña, formulario de login, iniciar sesión sin mostrar, meté la clave
activity: qué pasó, que paso, qué cambió, que cambio, quién hizo, quien hizo, historial, línea de tiempo, linea de tiempo, mientras no estaba, desde que me frenaste, qué hiciste, que hiciste, qué hicieron, que hicieron, registro, auditoría, auditoria
job_list: tareas, qué tareas, que tareas, trabajos, qué hay corriendo, que hay corriendo, en segundo plano

wait: esperar, esperá, aguardar, pausa
wait_for_idle: esperar a que termine, hasta que se calme, cuando pare
wait_for_window: esperar la ventana, hasta que abra la ventana
