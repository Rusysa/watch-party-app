# Weron v0.3.0, índice multiplataforma verificado en GHCR.
FROM ghcr.io/pojntfx/weron:v0.3.0@sha256:d286041a8e1ed23e08b65d8b8de2fecebba52204298908f916b5efc32d0911b7

# Sin DATABASE_URL ni REDIS_URL, Weron usa almacenamiento y broker en memoria.
USER 65532:65532
EXPOSE 15325

# exec permite que Weron reciba directamente SIGTERM al detener el contenedor.
CMD ["sh", "-c", "exec weron signaler --laddr :${PORT:-15325}"]
