module.exports = {
  apps: [
    {
      name: "pangu-sales-manager",
      cwd: "/home/fanghaizhou/pangu-sales-manager",
      script: "./deploy/run-server.sh",
      interpreter: "none",
      autorestart: true,
      max_restarts: 10,
      restart_delay: 2000,
      time: true
    }
  ]
};
