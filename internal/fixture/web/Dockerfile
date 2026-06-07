# UI service of the build-from-source fixture (#15). busybox httpd serves the
# site at / on :8080 — the proxy mounts this at the Session origin's "/".
FROM busybox:1.37
COPY index.html /www/index.html
EXPOSE 8080
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
