<template>
  <nav class="navbar" :class="themeClass">
    <div class="navbar-brand">
      <a class="navbar-item">Gaming VDI</a>
      <button class="button" @click="toggleTheme">{{ themeLabel }}</button>
    </div>
    <div class="navbar-menu">
      <div class="navbar-start">
        <span v-if="user" class="navbar-item">{{ user.username }}</span>
        <a v-if="user" class="navbar-item" @click="logout">Logout</a>
        <a v-else class="navbar-item" :href="loginUrl">Login</a>
      </div>
    </div>
  </nav>

  <section class="section" :class="themeClass">
    <div class="container">
      <h1 class="title">Games</h1>

      <!-- Roles & permissions: which actions each role is allowed -->
      <div class="box" :class="themeClass">
        <h2 class="subtitle">Role: {{ role }}</h2>
        <table class="table is-narrow is-fullwidth" :class="themeClass">
          <thead>
            <tr>
              <th>Role</th>
              <th>Allowed actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in roles" :key="r.role" :class="{ 'has-text-weight-bold': r.role === role }">
              <td>{{ r.role }}</td>
              <td>{{ r.actions }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="field">
        <label class="label">Search</label>
        <div class="control">
          <input class="input" v-model="query" placeholder="Search games…" />
        </div>
      </div>

      <div class="field is-grouped">
        <p class="control" v-if="can.override">
          <button class="button is-link" @click="showOverride = !showOverride">VM config</button>
        </p>
      </div>

      <div v-if="showOverride && can.override" class="box mt-4" :class="themeClass">
        <h2 class="subtitle">VM config override</h2>
        <div class="field">
          <label class="label">Session ID</label>
          <div class="control">
            <input class="input" type="number" v-model.number="override.id" />
          </div>
        </div>
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

      <!-- Admin: create games and start the worker queue -->
      <div v-if="can.manageGames" class="box mt-4" :class="themeClass">
        <h2 class="subtitle">Games (admin)</h2>
        <div class="field is-grouped">
          <div class="control">
            <button class="button is-info" @click="processLibrary">Process library</button>
          </div>
          <div class="control is-expanded">
            <p class="help">Starts the workers: they scan the library and process every game they find.</p>
          </div>
        </div>
        <div class="field is-grouped">
          <div class="control is-expanded">
            <input class="input" v-model="newGame.title" placeholder="Title" />
          </div>
          <div class="control">
            <input class="input" v-model="newGame.pegi_rating" placeholder="PEGI" />
          </div>
          <div class="control is-expanded">
            <input class="input" v-model="newGame.library_path" placeholder="Library path" />
          </div>
          <div class="control">
            <button class="button is-primary" @click="createGame">Create game</button>
          </div>
        </div>
        <table class="table is-fullwidth is-striped" :class="themeClass">
          <thead>
            <tr>
              <th>Title</th>
              <th>Library path</th>
              <th>PEGI</th>
              <th>Worker jobs</th>
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
              <td>
                <div class="buttons">
                  <button class="button is-small" @click="enqueueJob(g, 'config')">Generate config</button>
                  <button class="button is-small" @click="enqueueJob(g, 'metadata')">Fetch metadata</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Admin: user roles (who can play, who can administer) -->
      <div v-if="can.manageUsers" class="box mt-4" :class="themeClass">
        <h2 class="subtitle">Users (admin)</h2>
        <table class="table is-fullwidth is-striped" :class="themeClass">
          <thead>
            <tr>
              <th>Username</th>
              <th>Email</th>
              <th>Age</th>
              <th>Role</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in users" :key="u.id">
              <td>{{ u.username }}</td>
              <td>{{ u.email }}</td>
              <td>{{ u.age }}</td>
              <td>
                <div class="select is-small">
                  <select :value="u.role" @change="setRole(u, $event.target.value)">
                    <option value="anonymous">anonymous</option>
                    <option value="gamer">gamer</option>
                    <option value="admin">admin</option>
                  </select>
                </div>
              </td>
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
              <button v-if="can.play" class="button is-small is-primary" @click="play(g)">Play</button>
              <span v-else class="tag is-light">Sign in as gamer to play</span>
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
import { api, API_BASE } from "./api"

const loginUrl = API_BASE + "/auth/login"

const theme = ref(localStorage.getItem("theme") || "system")
const query = ref("")
const error = ref("")
const msg = ref("")
const allGames = ref([])
const user = ref(null)
const showOverride = ref(false)
const override = ref({ id: null, cpu_cores: 2, ram_size: 4096, disk_size: 128 })
const libraryEdits = ref({})
const users = ref([])
const newGame = ref({ title: "", pegi_rating: "", library_path: "" })

const roles = [
  { role: "anonymous", actions: "Browse games" },
  { role: "gamer", actions: "Browse games · Play · VM config override" },
  { role: "admin", actions: "Browse games · Play · VM config override · Manage games · Manage users" },
]

const role = computed(() => user.value?.role || "anonymous")
const can = computed(() => {
  const r = role.value
  const gamer = r === "gamer" || r === "admin"
  return {
    browse: true,
    play: gamer,
    override: gamer,
    manageGames: r === "admin",
    manageUsers: r === "admin",
  }
})

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

async function load() {
  try {
    const g = await api("/games")
    allGames.value = g
    g.forEach((game) => {
      libraryEdits.value[game.id] = game.library_path || ""
    })
  } catch (e) {
    error.value = e.message
    return
  }
  if (!localStorage.getItem("token")) return
  try {
    user.value = await api("/me")
    if (user.value.role === "admin") users.value = await api("/users")
  } catch {
    localStorage.removeItem("token")
    user.value = null
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
  const { id, ...body } = override.value
  try {
    await api("/sessions/" + id + "/override", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })
    msg.value = "Override applied"
  } catch (e) {
    error.value = e.message
  }
}

async function createGame() {
  error.value = ""
  msg.value = ""
  try {
    await api("/games", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(newGame.value),
    })
    newGame.value = { title: "", pegi_rating: "", library_path: "" }
    msg.value = "Game created"
    load()
  } catch (e) {
    error.value = e.message
  }
}

async function enqueueJob(g, kind) {
  error.value = ""
  msg.value = ""
  try {
    const job = await api("/games/" + g.id + "/jobs", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ kind }),
    })
    msg.value = "Job #" + job.id + " queued (" + kind + ")"
  } catch (e) {
    error.value = e.message
  }
}

async function processLibrary() {
  error.value = ""
  msg.value = ""
  try {
    const job = await api("/library/jobs", { method: "POST" })
    msg.value = "Library job #" + job.id + " queued: the workers process the games in the library"
  } catch (e) {
    error.value = e.message
  }
}

async function setRole(u, role) {
  error.value = ""
  msg.value = ""
  try {
    await api("/users/" + u.id, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ role }),
    })
    u.role = role
    msg.value = "Role updated"
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
  user.value = null
  location.hash = "#/login"
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
