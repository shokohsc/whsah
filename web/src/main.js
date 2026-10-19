import { createApp, h, ref } from "vue"
import App from "./App.vue"
import Login from "./Login.vue"

function currentPath() {
  return location.hash.replace(/^#\/?/, "").split("?")[0]
}

const Root = {
  setup() {
    const path = ref(currentPath())
    window.addEventListener("hashchange", () => (path.value = currentPath()))
    return () => h(path.value === "login" ? Login : App)
  },
}

createApp(Root).mount("#app")
