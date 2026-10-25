<template>
  <nav class="navbar netflix-nav" :class="themeClass">
    <div class="navbar-brand">
      <a class="navbar-item netflix-logo">Gaming VDI</a>
      <button class="button netflix-toggle" @click="toggleTheme">{{ themeLabel }}</button>
    </div>
    <div class="navbar-menu">
      <div class="navbar-start">
        <span v-if="user" class="navbar-item">{{ user.username }}</span>
        <a v-if="user" class="navbar-item" @click="logout">Logout</a>
        <a v-else class="navbar-item" :href="loginUrl">Login</a>
      </div>
    </div>
  </nav>

  <section class="section netflix-hero" :class="themeClass">
    <div class="container is-fluid">
      <h1 class="title netflix-title">Browse</h1>

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

        <label class="label">Library paths</label>
        <div v-for="p in libraryPaths" :key="p.id" class="field is-grouped">
          <div class="control">
            <span class="tag is-info">{{ p.category }}</span>
          </div>
          <div class="control is-expanded">{{ p.path }}</div>
          <div class="control">
            <button class="button is-small is-danger" @click="removeLibraryPath(p)">Remove</button>
          </div>
        </div>
        <div class="field is-grouped">
          <div class="control">
            <div class="select">
              <select v-model="newPath.category">
                <option v-for="c in categories" :key="c" :value="c">{{ c }}</option>
              </select>
            </div>
          </div>
          <div class="control is-expanded">
            <input class="input" v-model="newPath.path" placeholder="/games/repacks" />
          </div>
          <div class="control">
            <button class="button is-small is-primary" @click="addLibraryPath">Add path</button>
          </div>
        </div>

        <div class="field is-grouped">
          <div class="control">
            <button class="button is-info" @click="processLibrary">Process library</button>
          </div>
          <div class="control">
            <button class="button is-danger" @click="resetLibrary">Reset library</button>
          </div>
          <div class="control is-expanded">
            <p class="help">Starts the workers: they scan every configured path and process the game folders they find. Reset deletes all games and jobs so the library can be rebuilt from scratch.</p>
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
              <th>Category</th>
              <th>Library path</th>
              <th>PEGI</th>
              <th>Worker jobs</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="g in allGames" :key="g.id">
              <td>{{ displayTitle(g) }}</td>
              <td>{{ g.category || "—" }}</td>
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

      <!-- Admin: clone a Proxmox template with custom VM specs -->
      <div v-if="can.manageGames" class="box mt-4" :class="themeClass">
        <h2 class="subtitle">Custom VM (admin)</h2>
        <div class="field is-grouped is-grouped-multiline">
          <div class="control">
            <input class="input" type="number" v-model.number="newVM.template_id" placeholder="Template VMID" />
          </div>
          <div class="control is-expanded">
            <input class="input" v-model="newVM.name" placeholder="VM name (letters, digits, hyphens)" />
          </div>
        </div>
        <div class="field is-grouped is-grouped-multiline">
          <div class="control">
            <input class="input" type="number" v-model.number="newVM.cpu_cores" placeholder="CPU cores" />
          </div>
          <div class="control">
            <input class="input" type="number" v-model.number="newVM.ram_size" placeholder="RAM (MB)" />
          </div>
          <div class="control">
            <input class="input" type="number" v-model.number="newVM.disk_size" placeholder="Disk (GB)" />
          </div>
          <div class="control">
            <div class="select">
              <select v-model="newVM.vgpu">
                <option value="">VGA: template</option>
                <option v-for="v in vgpuOptions" :key="v" :value="v">VGA: {{ v }}</option>
              </select>
            </div>
          </div>
          <div class="control">
            <button class="button is-primary" @click="createVM">Create VM</button>
          </div>
        </div>
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

      <div class="netflix-row">
        <div class="netflix-grid">
          <div v-for="g in pagedGames" :key="g.id" class="netflix-card">
            <div class="netflix-card-inner">
              <div class="netflix-card-poster">
                <div class="netflix-card-title">{{ displayTitle(g) }}</div>
              </div>
              <div class="netflix-card-info">
                <div class="netflix-card-meta">
                  <span v-for="genre in metaFor(g).genres.slice(0, 3)" :key="genre" class="tag">{{ genre }}</span>
                  <span v-if="metaFor(g).year" class="tag is-dark">{{ metaFor(g).year }}</span>
                  <span v-if="metaFor(g).rating" class="tag is-dark">{{ metaFor(g).rating }}</span>
                  <span class="tag is-dark">{{ metaFor(g).pegi || g.pegi_rating }}</span>
                </div>
                <div class="netflix-card-actions">
                  <button v-if="can.play" class="button is-small is-danger netflix-play" @click="play(g)">▶ Play</button>
                  <span v-else class="tag is-light">Sign in to play</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <nav v-if="pageCount > 1" class="pagination is-small netflix-pagination mt-5" role="navigation" aria-label="Game pages">
        <button type="button" class="pagination-previous" :disabled="page <= 1" @click="page--">Previous</button>
        <button type="button" class="pagination-next" :disabled="page >= pageCount" @click="page++">Next</button>
        <ul class="pagination-list">
          <li><span class="pagination-link is-current">Page {{ page }} of {{ pageCount }}</span></li>
        </ul>
      </nav>

      <div v-if="error" class="notification is-danger mt-4">{{ error }}</div>
      <div v-if="msg" class="notification is-info mt-4">{{ msg }}</div>
    </div>
  </section>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, watch } from "vue"
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
const libraryPaths = ref([])
const categories = ["repacks", "portables", "isos", "setups"]
const newPath = ref({ category: "repacks", path: "" })
const users = ref([])
const newGame = ref({ title: "", pegi_rating: "", library_path: "" })
const newVM = ref({ template_id: null, name: "", cpu_cores: 4, ram_size: 8192, disk_size: 128, vgpu: "virtio" })
const vgpuOptions = ["std", "cirrus", "vmware", "qxl", "virtio", "none"]
const page = ref(1)
const pageSize = 24

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
const filteredGames = computed(() => {
  const q = query.value.toLowerCase()
  return allGames.value.filter((g) => displayTitle(g).toLowerCase().includes(q))
})
const pageCount = computed(() => Math.max(1, Math.ceil(filteredGames.value.length / pageSize)))
const pagedGames = computed(() => {
  const start = (page.value - 1) * pageSize
  return filteredGames.value.slice(start, start + pageSize)
})
watch(query, () => { page.value = 1 })
watch(pageCount, (count) => {
  if (page.value > count) page.value = count
})

// The server exposes the sanitized title (models.SanitizeGameName) next to the
// raw folder/file name, so the UI never re-implements that logic.
function displayTitle(g) {
  return (g && (g.sanitized_title || g.title)) || ""
}

// Parse the remote-source metadata stored on the game row. Database fields
// (title, pegi_rating, category) are only fallbacks; the catalog presents the
// merged fitgirl/rawg/steam/... data from Game.metadata.
function metaFor(g) {
  const out = { genres: [], year: "", rating: "", pegi: "" }
  if (!g || !g.metadata) return out
  try {
    const p = JSON.parse(g.metadata)
    if (Array.isArray(p.genres)) out.genres = p.genres
    if (p.release_date) out.year = String(p.release_date).slice(0, 4)
    if (p.metacritic_score) out.rating = String(p.metacritic_score)
    if (p.pegi_rating) out.pegi = p.pegi_rating
  } catch {}
  return out
}

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
    if (user.value.role === "admin") {
      users.value = await api("/users")
      libraryPaths.value = await api("/library/paths")
    }
  } catch {
    localStorage.removeItem("token")
    user.value = null
  }
}

onMounted(() => {
  setTheme()
  load()
})

let heartbeat = null

async function play(g) {
  error.value = ""
  msg.value = ""
  try {
    const s = await api("/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ game_id: g.id }),
    })
    msg.value = "Session created: " + s.vm_id
    if (heartbeat) clearInterval(heartbeat)
    heartbeat = setInterval(() => {
      api("/sessions/" + s.id + "/heartbeat", { method: "POST" }).catch(() => {})
    }, 5 * 60 * 1000)
  } catch (e) {
    error.value = e.message
  }
}

onBeforeUnmount(() => {
  if (heartbeat) clearInterval(heartbeat)
})

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

async function createVM() {
  error.value = ""
  msg.value = ""
  try {
    const vm = await api("/admin/vms", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(newVM.value),
    })
    msg.value = "VM clone #" + vm.vmid + " created"
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

async function addLibraryPath() {
  error.value = ""
  msg.value = ""
  try {
    await api("/library/paths", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(newPath.value),
    })
    newPath.value.path = ""
    msg.value = "Library path added"
    libraryPaths.value = await api("/library/paths")
  } catch (e) {
    error.value = e.message
  }
}

async function removeLibraryPath(p) {
  error.value = ""
  msg.value = ""
  try {
    await api("/library/paths/" + p.id, { method: "DELETE" })
    msg.value = "Library path removed"
    libraryPaths.value = await api("/library/paths")
  } catch (e) {
    error.value = e.message
  }
}

async function resetLibrary() {
  error.value = ""
  msg.value = ""
  if (!confirm("Delete all games and queued jobs? Configured paths are kept.")) return
  try {
    await api("/library", { method: "DELETE" })
    msg.value = "Library reset"
    await load()
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
/* Netflix-inspired theme */
.netflix-nav {
  background: linear-gradient(180deg, rgba(0,0,0,0.9) 0%, rgba(0,0,0,0.7) 100%);
  border: none;
}
.netflix-logo {
  color: #e50914 !important;
  font-weight: 800;
  font-size: 1.5rem;
  letter-spacing: 0.05em;
}
.netflix-toggle {
  background: transparent;
  color: #fff;
  border: 1px solid rgba(255,255,255,0.5);
}
.netflix-hero {
  background: radial-gradient(ellipse at top, #141414 0%, #000000 100%);
  min-height: 100vh;
  padding-top: 2rem;
}
.netflix-title {
  color: #fff;
  font-size: 2rem;
  font-weight: 700;
  margin-bottom: 1.5rem;
}
.netflix-row {
  margin-top: 2rem;
}
.netflix-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 1rem;
}
.netflix-card {
  position: relative;
  transition: transform 0.2s ease-in-out;
  cursor: pointer;
}
.netflix-card:hover {
  transform: scale(1.05);
  z-index: 10;
}
.netflix-card-inner {
  background: #181818;
  border-radius: 4px;
  overflow: hidden;
  box-shadow: 0 4px 6px rgba(0,0,0,0.5);
}
.netflix-card-poster {
  height: 280px;
  background: linear-gradient(135deg, #e50914 0%, #831010 100%);
  display: flex;
  align-items: flex-end;
  padding: 1rem;
}
.netflix-card-title {
  color: #fff;
  font-weight: 600;
  font-size: 0.9rem;
  text-shadow: 2px 2px 4px rgba(0,0,0,0.8);
}
.netflix-card-info {
  padding: 0.75rem;
  background: #181818;
}
.netflix-card-meta {
  display: flex;
  gap: 0.5rem;
  margin-bottom: 0.5rem;
  flex-wrap: wrap;
}
.netflix-card-actions {
  display: flex;
  gap: 0.5rem;
}
.netflix-play {
  background: #e50914;
  border: none;
  color: #fff;
  font-weight: 600;
}
.netflix-play:hover {
  background: #f40612;
}
.netflix-pagination {
  justify-content: center;
}
.netflix-pagination .pagination-link.is-current {
  background: #e50914;
  border-color: #e50914;
  color: #fff;
}

/* Bulma dark theme support */
.is-dark-theme {
  background: #000000;
  color: #ddd;
}
.is-dark-theme .table td,
.is-dark-theme .table th {
  border-color: #333;
  background: #141414;
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
.is-dark-theme .label {
  color: #ddd;
}
</style>
