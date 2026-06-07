# API service of the build-from-source fixture (#15). busybox httpd serves under
# /api on :8080 — the proxy mounts this at the Session origin's "/api" prefix
# (same-origin) and forwards the prefix unstripped, so the API answers at /api/*.
FROM busybox:1.37
COPY index.html /www/api/index.html
EXPOSE 8080
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
