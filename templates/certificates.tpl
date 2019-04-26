{{ if .IsBootstrap }}

sudo mkdir -p /etc/kubernetes
sudo mkdir -p /etc/kubernetes/pki
sudo mkdir -p /etc/kubernetes/pki/etcd

# Use CA generated on control side
sudo bash -c "cat > /etc/kubernetes/pki/ca.crt <<EOF
{{ .CACert }}EOF"

{{ end }}