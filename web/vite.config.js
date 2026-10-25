import { defineConfig } from "vite"
import vue from "@vitejs/plugin-vue"

export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      "^/(auth|games|sessions|jobs|users|me|config|health|agent)": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
})
