<template>
  <section class="section">
    <div class="container">
      <h1 class="title">Gaming VDI</h1>
      <div v-if="error" class="notification is-danger">{{ error }}</div>
      <a class="button is-primary" :href="loginUrl">Sign in</a>
    </div>
  </section>
</template>

<script setup>
import { ref, onMounted } from "vue"
import { API_BASE } from "./api"

const loginUrl = API_BASE + "/auth/login"
const error = ref("")

onMounted(() => {
  const params = new URLSearchParams(location.hash.split("?")[1] || "")
  error.value = params.get("error") || ""
  const token = params.get("token")
  if (token) {
    localStorage.setItem("token", token)
    location.replace(location.pathname + location.search)
  }
})
</script>
