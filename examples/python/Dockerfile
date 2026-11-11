FROM python:3.14-slim

WORKDIR /app
COPY examples/python/service.py .
USER 65532:65532
EXPOSE 8086
ENTRYPOINT ["python", "/app/service.py"]
