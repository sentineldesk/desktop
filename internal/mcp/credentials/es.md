# Credential vocabulary — Spanish

Deliberadamente no es una traducción de en.md. Traducir `connection string` da
una frase que nadie escribe; lo que está acá es lo que la gente realmente pone
en un archivo de configuración o tipea en una terminal.

Dos cosas que un archivo traducido palabra por palabra perdería:

Las formas SIN acento. `contrasena` y `contraseña` conviven en el mismo
proyecto — una variable de entorno rara vez lleva la eñe — y la que se escapa
es siempre la que no se pensó. Ambas están.

Los placeholders en español. Esta es la parte que faltaba y que importa más de
lo que parece: los valores de relleno estaban solo en inglés, así que
`clave = cambiame` en un config de ejemplo daba falsa alarma. Un detector que
grita con la configuración de ejemplo de la propia máquina es un detector que
alguien apaga la primera semana.

Un término por línea o separados por comas; `#` empieza un comentario.

## names

contraseña, contrasena, contraseñas, contrasenas, contrasena, clave, claves
clave secreta, clave privada, clave publica, clave pública, clave de acceso
clave maestra, contraseña maestra, contrasena maestra, clave de cifrado
secreto, secretos, secreto compartido, palabra secreta
credencial, credenciales, usuario y clave, usuario y contraseña
llave, llaves, llave privada, llave secreta, llave de acceso
token, token de acceso, token de sesion, token de sesión, token de autenticacion
frase de paso, frase secreta, frase de acceso
codigo de acceso, código de acceso, codigo secreto, código secreto
cadena de conexion, cadena de conexión, url de base de datos
clave de licencia, clave de activacion, clave de activación
codigo de recuperacion, código de recuperación, codigo de respaldo
clave de root, clave de administrador, clave de admin, clave de sudo
clave del correo, clave de la base, clave de la bd

## phrases

la contraseña es, la contrasena es, la clave es, mi contraseña es
mi clave es, la contraseña era, la clave era, el secreto es
la llave es, el token es, la frase es
entra con, entrá con, ingresa con, ingresá con, conectate con, conéctate con
usa la clave, usá la clave, usa la contraseña, usá la contraseña
con la clave, con la contraseña, usuario y clave, usuario y contraseña
te paso la clave, te paso la contraseña, anota la clave

## placeholders

cambiar, cambiame, cambiamé, cambialo, cambiar esto, cambiar_esto
cambia esto, cambiá esto, poner clave, poner la clave, poné la clave
tu clave, tuclave, tu_clave, tu contraseña, tu contrasena, tucontrasena
mi clave, miclave, mi contraseña, micontrasena
ejemplo, ejemplos, prueba, pruebas, muestra, relleno
oculto, oculta, redactado, tapado, censurado, quitado
sin definir, sin valor, no definido, nodefinido, vacio, vacío, nada
ninguna, ninguno, pendiente, completar, rellenar, definir
aqui va, aquí va, poner aqui, poner aquí, reemplazar, reemplazame
contraseña, contrasena, clave, secreto, credenciales, llave
