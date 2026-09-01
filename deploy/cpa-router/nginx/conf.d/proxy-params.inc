proxy_http_version 1.1;

proxy_set_header Host $http_host;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection $connection_upgrade;

proxy_request_buffering off;
proxy_buffering off;
proxy_cache off;
proxy_intercept_errors off;
proxy_next_upstream off;

client_max_body_size 0;
client_body_timeout 24h;
proxy_read_timeout 24h;
proxy_send_timeout 24h;
send_timeout 24h;
