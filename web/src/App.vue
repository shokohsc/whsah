<template>
  <nav class="navbar" :class="themeClass">
    <div class="navbar-brand">
      <a class="navbar-item">Gaming VDI</a>
      <button class="button" @click="toggleTheme">{{ themeLabel }}</button>
    </div>
    <div class="navbar-menu">
      <div class="navbar-start">
        <a class="navbar-item" @click="logout">Logout</a>
      </div>
    </div>
  </nav>

  <section class="section" :class="themeClass">
    <div class="container">
      <h1 class="title">Games</h1>
      <div class="field">
        <label class="label">Search</label>
        <div class="control">
          <input class="input" v-model="query" placeholder="Search games…" @input="filter" />
        </div>
      </div>

      <div class="field is-grouped">
        <p class="control">
          <button class="button is-primary" @click="browse">Browse</button>
        </p>
        <p class="control" v-if="user">
          <button class="button is-link" @click="showOverride = !showOverride">VM config</button>
        </p>
      </div>

      <div v-if="showOverride && user" class="box mt-4" :class="themeClass">
        <h2 class="subtitle">VM config override</h2>
        <div class="field">
          <label class="label">CPU cores</label>
          <div class="control">
            <input class="input" type="number" v-model.number="override.cpu_cores" />
          </div>
        </div>
        <div class="field">
          <label class="label">RAM (MB)</label>
          <div class="control">
            <input class="input" type="number" v-model.number="override.ram_size" />
          </div>
        </div>
        <div class="field">
          <label class="label">Disk (GB)</label>
          <div class="control">
            <input class="input" type="number" v-model.number="override.disk_size" />
          </div>
        </div>
        <p class="control">
          <button class="button is-success" @click="applyOverride">Apply</button>
        </p>
      </div>

      <!-- Admin: game library path management -->
      <div v-if="isAdmin" class="box mt-4" :class="themeClass">
        <h2 class="subtitle">Game library paths (admin)</h2>
        <table class="table is-fullwidth is-striped" :class="themeClass">
          <thead>
            <tr>
              <th>Title</th>
              <th>Library path</th>
              <th>PEGI</th>
              <th>Action</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="g in allGames" :key="g.id">
              <td>{{ g.title }}</td>
              <td>
                <div class="field is-grouped">
                  <div class="control is-expanded">
                    <input class="input" type="text" v-model="libraryEdits[g.id]" />
                  </div>
                  <div class="control">
                    <button class="button is-small is-success" @click="saveLibraryPath(g)">Save</button>
                  </div>
                </div>
              </td>
              <td>{{ g.pegi_rating }}</td>
              <td></td>
            </tr>
          </tbody>
        </table>
      </div>

      <table class="table is-fullwidth is-striped mt-4" :class="themeClass">
        <thead>
          <tr>
            <th>Title</th>
            <th>PEGI</th>
            <th>CPU</th>
            <th>RAM</th>
            <th>Disk</th>
            <th>Action</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="g in visibleGames" :key="g.id">
            <td>{{ g.title }}</td>
            <td>{{ g.pegi_rating }}</td>
            <td>{{ g.vm_config.cpu_cores }}</td>
            <td>{{ g.vm_config.ram_size }} MB</td>
            <td>{{ g.vm_config.disk_size }} GB</td>
            <td>
              <button class="button is-small is-primary" @click="play(g)">Play</button>
            </td>
          </tr>
        </tbody>
      </table>

      <div v-if="error" class="notification is-danger mt-4">{{ error }}</div>
      <div v-if="msg" class="notification is-info mt-4">{{ msg }}</div>
    </div>
  </section>
</template>

<script setup>
import { ref, computed, onMounted } from "vue"

const theme = ref(localStorage.getItem("theme") || "system")
const query = ref("")
const error = ref("")
const msg = ref("")
const games = ref([])
const allGames = ref([])
const user = ref(null)
const isAdmin = ref(false)
const showOverride = ref(false)
const override = ref({ cpu_cores: 2, ram_size: 4096, disk_size: 128 })
const libraryEdits = ref({})

const themeClass = computed(() => {
  const t = theme.value
  if (t === "system") {
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "is-dark-theme" : ""
  }
  return t === "dark" ? "is-dark-theme" : ""
})
const themeLabel = computed(() => (theme.value === "dark" ? "Light" : "Dark"))
const visibleGames = computed(() => {
  const q = query.value.toLowerCase()
  return allGames.value.filter((g) => g.title.toLowerCase().includes(q))
})

function toggleTheme() {
  const next = theme.value === "dark" ? "light" : "dark"
  theme.value = next
  localStorage.setItem("theme", next)
}

window
  .matchMedia("(prefers-color-scheme: dark)")
  .addEventListener("change", () => {
    if (theme.value === "system") document.documentElement.className = themeClass.value
  })

function setTheme() {
  document.documentElement.className = themeClass.value
}

async function api(path, opts = {}) {
  const headers = { ...opts.headers }
  const token = localStorage.getItem("token")
  if (token) headers.Authorization = "Bearer " + token
  const res = await fetch("/api" + path, { ...opts, headers })
  if (!res.ok) throw new Error(await res.text())
  return res.json()
}

async function load() {
  try {
    const g = await api("/games")
    allGames.value = g
    games.value = g
    g.forEach((game) => {
      libraryEdits.value[game.id] = game.library_path || ""
    })
    const me = await api("/me")
    user.value = me
    isAdmin.value = me.role === "admin"
  } catch (e) {
    error.value = e.message
  }
}

onMounted(() => {
  setTheme()
  load()
})

async function play(g) {
  error.value = ""
  msg.value = ""
  try {
    const s = await api("/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ game_id: g.id, vm_id: String(g.id) }),
    })
    msg.value = "Session created: " + s.vm_id
  } catch (e) {
    error.value = e.message
  }
}

async function applyOverride() {
  error.value = ""
  msg.value = ""
  try {
    await api("/sessions/override", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(override.value),
    })
    msg.value = "Override applied"
  } catch (e) {
    error.value = e.message
  }
}

async function saveLibraryPath(g) {
  error.value = ""
  msg.value = ""
  try {
    await api("/games/" + g.id, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ library_path: libraryEdits.value[g.id] }),
    })
    msg.value = "Library path updated"
  } catch (e) {
    error.value = e.message
  }
}

function logout() {
  localStorage.removeItem("token")
  localStorage.removeItem("user")
  location.reload()
}
</script>

<style>
/* Bulma dark theme support */
.is-dark-theme {
  background: #1a1a1a;
  color: #ddd;
}
.is-dark-theme .table td,
.is-dark-theme .table th {
  border-color: #444;
  background: #222;
  color: #ddd;
}
.is-dark-theme .box,
.is-dark-theme .input,
.is-dark-theme .select select {
  background: #2a2a2a;
  color: #ddd;
  border-color: #444;
}
.is-dark-theme .notification {
  background: #2a2a2a;
  border-color: #444;
}
</style>
