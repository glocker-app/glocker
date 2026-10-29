# Glocker Android Architecture

This document outlines the architecture for an Android port of Glocker, based on analysis of BlockerX and Android platform capabilities.

## Overview

Glocker Android will be a native Kotlin app using modern Android architecture (Jetpack, Coroutines, Compose) to provide distraction blocking on Android devices without requiring root access.

## Key Architectural Decisions

### 1. Primary Blocking Mechanism: AccessibilityService

Unlike the Linux version which uses `/etc/hosts` and iptables, Android will use **AccessibilityService** as the primary blocking mechanism.

**Why AccessibilityService?**
- Can monitor URLs in browsers (all browsers, not just one)
- Can read visible text for keyword detection
- Works without root or VPN
- No battery drain from packet inspection
- Proven approach (used by BlockerX successfully)

**Trade-offs:**
- User must manually enable in Android Settings → Accessibility
- Privacy concern (can theoretically read everything on screen)
- Can be disabled by determined users (mitigated by Device Admin)

### 2. Optional VPN for Enhanced Blocking

VPN will be **optional** (not mandatory like initially planned):
- Provides DNS-level blocking for non-browser apps
- Better for blocking apps that don't use standard browsers
- Reduces false negatives
- User can choose based on battery/trust preferences

### 3. Device Admin for Tamper Protection

Use Android Device Administrator API to:
- Prevent uninstallation without password
- Require manual Settings navigation to disable
- Send notification to accountability partner on tamper

### 4. Access Code System (Superior to Email)

Replace email notifications with **access code system**:
- Generate random code when user wants to disable
- Code sent to accountability partner in real-time
- User cannot proceed without entering partner's code
- Much harder to bypass than email notifications

## System Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    Glocker Android                      │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌─────────────────────────────────────────────────┐  │
│  │           UI Layer (Jetpack Compose)            │  │
│  ├─────────────────────────────────────────────────┤  │
│  │  • Main Dashboard (status, violations)          │  │
│  │  • Domain Management (add/remove/import)        │  │
│  │  • Time Windows Configuration                   │  │
│  │  • Accountability Partner Setup                 │  │
│  │  • Settings & Preferences                       │  │
│  └─────────────────────────────────────────────────┘  │
│                         │                              │
│  ┌─────────────────────────────────────────────────┐  │
│  │          ViewModel Layer (Architecture)         │  │
│  ├─────────────────────────────────────────────────┤  │
│  │  • MainViewModel (status, state)                │  │
│  │  • DomainViewModel (CRUD operations)            │  │
│  │  • TimeWindowViewModel (schedule management)    │  │
│  │  • AccountabilityViewModel (partner comms)      │  │
│  └─────────────────────────────────────────────────┘  │
│                         │                              │
│  ┌─────────────────────────────────────────────────┐  │
│  │         Repository Layer (Data Access)          │  │
│  ├─────────────────────────────────────────────────┤  │
│  │  • DomainRepository (blocked domains DB)        │  │
│  │  • ViolationRepository (violation tracking)     │  │
│  │  • ConfigRepository (settings persistence)      │  │
│  │  • UnblockRepository (temp unblock log)         │  │
│  └─────────────────────────────────────────────────┘  │
│                         │                              │
│  ┌─────────────────────────────────────────────────┐  │
│  │            Service Layer (Background)           │  │
│  ├─────────────────────────────────────────────────┤  │
│  │                                                 │  │
│  │  ┌──────────────────────────────────────────┐  │  │
│  │  │   GlockerAccessibilityService (PRIMARY)  │  │  │
│  │  ├──────────────────────────────────────────┤  │  │
│  │  │  • Monitor active window                 │  │  │
│  │  │  • Extract URLs from browsers            │  │  │
│  │  │  • Scan visible text for keywords        │  │  │
│  │  │  • Detect app launches                   │  │  │
│  │  │  • Show blocking overlay                 │  │  │
│  │  └──────────────────────────────────────────┘  │  │
│  │                                                 │  │
│  │  ┌──────────────────────────────────────────┐  │  │
│  │  │   GlockerVpnService (OPTIONAL)           │  │  │
│  │  ├──────────────────────────────────────────┤  │  │
│  │  │  • Intercept network packets             │  │  │
│  │  │  • Block at DNS level                    │  │  │
│  │  │  • Enforce safe search                   │  │  │
│  │  │  • Work with non-browser apps            │  │  │
│  │  └──────────────────────────────────────────┘  │  │
│  │                                                 │  │
│  │  ┌──────────────────────────────────────────┐  │  │
│  │  │   EnforcementForegroundService           │  │  │
│  │  ├──────────────────────────────────────────┤  │  │
│  │  │  • Periodic enforcement checks (60s)     │  │  │
│  │  │  • Time window evaluation                │  │  │
│  │  │  • Temp unblock expiration               │  │  │
│  │  │  • Violation threshold monitoring        │  │  │
│  │  └──────────────────────────────────────────┘  │  │
│  │                                                 │  │
│  └─────────────────────────────────────────────────┘  │
│                         │                              │
│  ┌─────────────────────────────────────────────────┐  │
│  │          Blocking Engine (Core Logic)           │  │
│  ├─────────────────────────────────────────────────┤  │
│  │  • Domain matching (same as Linux)              │  │
│  │  • Keyword detection (regex/simple match)       │  │
│  │  • Time window evaluation (ported from Go)      │  │
│  │  • Temp unblock management (same logic)         │  │
│  │  • Violation tracking & thresholds              │  │
│  └─────────────────────────────────────────────────┘  │
│                         │                              │
│  ┌─────────────────────────────────────────────────┐  │
│  │         Tamper Protection Layer                 │  │
│  ├─────────────────────────────────────────────────┤  │
│  │  • Device Admin (uninstall protection)          │  │
│  │  • Access code generation & validation          │  │
│  │  • Partner notification on tamper               │  │
│  │  • Self-integrity checks                        │  │
│  └─────────────────────────────────────────────────┘  │
│                         │                              │
│  ┌─────────────────────────────────────────────────┐  │
│  │           Data Layer (Persistence)              │  │
│  ├─────────────────────────────────────────────────┤  │
│  │  • Room Database (domains, violations, logs)    │  │
│  │  • SharedPreferences (settings, flags)          │  │
│  │  • File storage (import/export config)          │  │
│  └─────────────────────────────────────────────────┘  │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

## Core Components

### 1. GlockerAccessibilityService

**Purpose:** Primary blocking mechanism for browser URLs and page content.

**Responsibilities:**
- Monitor `TYPE_WINDOW_CONTENT_CHANGED` events
- Extract URLs from browser address bars (Chrome, Firefox, Brave, etc.)
- Scan visible text nodes for blocked keywords
- Detect when blocked content is accessed
- Show system overlay blocking screen
- Record violations to database

**Implementation:**
```kotlin
class GlockerAccessibilityService : AccessibilityService() {

    override fun onAccessibilityEvent(event: AccessibilityEvent) {
        when (event.eventType) {
            TYPE_WINDOW_CONTENT_CHANGED -> {
                val rootNode = event.source ?: return

                // Extract URL from browser
                val url = extractBrowserUrl(rootNode)

                // Scan visible text for keywords
                val text = extractVisibleText(rootNode)

                // Check if blocked
                if (isBlocked(url, text)) {
                    showBlockingOverlay(url)
                    recordViolation(url, "content_access")
                }

                rootNode.recycle()
            }

            TYPE_WINDOW_STATE_CHANGED -> {
                // Detect app launches for app blocking
                handleAppLaunch(event)
            }
        }
    }

    private fun extractBrowserUrl(node: AccessibilityNodeInfo): String? {
        // Search for URL bar based on browser type
        return when (currentBrowserPackage) {
            "com.android.chrome" -> findChromeUrlBar(node)
            "org.mozilla.firefox" -> findFirefoxUrlBar(node)
            "com.brave.browser" -> findBraveUrlBar(node)
            else -> findGenericUrlBar(node)
        }
    }

    private fun extractVisibleText(node: AccessibilityNodeInfo): String {
        val textBuilder = StringBuilder()
        traverseNodes(node) { childNode ->
            childNode.text?.let { textBuilder.append(it).append(" ") }
        }
        return textBuilder.toString()
    }
}
```

**Configuration:**
```xml
<!-- res/xml/accessibility_service_config.xml -->
<accessibility-service
    android:accessibilityEventTypes="typeWindowContentChanged|typeWindowStateChanged"
    android:accessibilityFeedbackType="feedbackGeneric"
    android:accessibilityFlags="flagReportViewIds|flagRetrieveInteractiveWindows"
    android:canRetrieveWindowContent="true"
    android:description="@string/accessibility_service_description"
    android:notificationTimeout="100" />
```

### 2. GlockerVpnService (Optional)

**Purpose:** DNS-level blocking for non-browser apps and enhanced blocking.

**Responsibilities:**
- Create VPN tunnel (TUN interface)
- Intercept DNS queries
- Return NXDOMAIN or 127.0.0.1 for blocked domains
- Block at packet level before connection
- Handle IPv4 and IPv6
- Minimal battery impact

**Implementation:**
```kotlin
class GlockerVpnService : VpnService() {

    private var vpnInterface: ParcelFileDescriptor? = null
    private val blockedDomains = ConcurrentHashMap<String, Boolean>()

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // Create VPN interface
        vpnInterface = Builder()
            .setSession("Glocker")
            .addAddress("10.0.0.2", 24)
            .addRoute("0.0.0.0", 0)
            .addDnsServer("1.1.1.1")
            .establish()

        // Start packet processing
        startPacketProcessing()

        return START_STICKY
    }

    private fun startPacketProcessing() {
        scope.launch(Dispatchers.IO) {
            val buffer = ByteBuffer.allocate(32767)

            while (isActive) {
                val length = vpnInterface?.fileDescriptor?.read(buffer.array()) ?: -1
                if (length > 0) {
                    buffer.limit(length)
                    processPacket(buffer)
                    buffer.clear()
                }
            }
        }
    }

    private fun processPacket(packet: ByteBuffer) {
        // Parse IP header
        val version = (packet.get(0).toInt() shr 4) and 0x0F

        if (version == 4) {
            processIPv4Packet(packet)
        } else if (version == 6) {
            processIPv6Packet(packet)
        }
    }

    private fun processIPv4Packet(packet: ByteBuffer) {
        // Extract destination IP
        val destIp = extractDestIP(packet)

        // Check if blocked domain resolves to this IP
        if (isBlockedIP(destIp)) {
            // Drop packet (don't write back to tunnel)
            recordViolation(destIp, "network_access")
            return
        }

        // Forward packet
        vpnInterface?.fileDescriptor?.write(packet.array(), 0, packet.limit())
    }
}
```

### 3. EnforcementForegroundService

**Purpose:** Periodic enforcement checks (like Linux daemon).

**Responsibilities:**
- Run every 60 seconds
- Evaluate time windows (enter/exit blocking periods)
- Check temp unblock expirations
- Monitor violation thresholds
- Execute configured commands
- Maintain persistent notification (Android requirement)

**Implementation:**
```kotlin
class EnforcementForegroundService : Service() {

    private val scope = CoroutineScope(Dispatchers.Default + SupervisorJob())

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForeground(NOTIFICATION_ID, createNotification())
        startEnforcementLoop()
        return START_STICKY
    }

    private fun startEnforcementLoop() {
        scope.launch {
            while (isActive) {
                performEnforcementCheck()
                delay(60_000) // 60 seconds
            }
        }
    }

    private suspend fun performEnforcementCheck() {
        val now = System.currentTimeMillis()

        // 1. Evaluate time windows
        checkTimeWindows(now)

        // 2. Clean expired temp unblocks
        cleanupExpiredUnblocks(now)

        // 3. Check violation thresholds
        checkViolationThresholds()

        // 4. Update notification
        updateNotification()
    }

    private suspend fun checkTimeWindows(now: Long) {
        val domains = domainRepository.getTimeWindowDomains()

        domains.forEach { domain ->
            val wasBlocked = domain.currentlyBlocked
            val shouldBlock = evaluateTimeWindows(domain, now)

            if (wasBlocked != shouldBlock) {
                // State changed
                handleBlockingStateChange(domain, shouldBlock)
            }
        }
    }
}
```

### 4. Device Admin Receiver

**Purpose:** Prevent uninstallation and detect tampering.

**Implementation:**
```kotlin
class GlockerDeviceAdminReceiver : DeviceAdminReceiver() {

    override fun onEnabled(context: Context, intent: Intent) {
        super.onEnabled(context, intent)
        Log.i("Glocker", "Device Admin enabled - uninstall protection active")
    }

    override fun onDisableRequested(context: Context, intent: Intent): CharSequence {
        // User trying to disable device admin
        return "Disabling device admin will allow uninstallation. " +
               "Your accountability partner will be notified."
    }

    override fun onDisabled(context: Context, intent: Intent) {
        super.onDisabled(context, intent)

        // Send notification to accountability partner
        notifyAccountabilityPartner(
            "Device Admin Disabled",
            "Glocker uninstall protection has been disabled."
        )
    }
}
```

### 5. Access Code System

**Purpose:** Require accountability partner approval to disable blocking.

**Implementation:**
```kotlin
class AccessCodeManager(
    private val accountabilityRepo: AccountabilityRepository,
    private val notificationService: NotificationService
) {

    suspend fun requestDisable(reason: String): AccessCodeResult {
        // Generate 6-digit code
        val code = generateAccessCode()

        // Store with expiration
        val request = AccessCodeRequest(
            code = code,
            reason = reason,
            timestamp = System.currentTimeMillis(),
            expiresAt = System.currentTimeMillis() + TimeUnit.MINUTES.toMillis(10)
        )

        accessCodeRepo.saveRequest(request)

        // Send to accountability partner
        val partner = accountabilityRepo.getPartner()
        notificationService.sendAccessCode(
            partner = partner,
            code = code,
            reason = reason
        )

        return AccessCodeResult.CodeSent(expiresInMinutes = 10)
    }

    suspend fun validateCode(userEnteredCode: String): Boolean {
        val activeRequest = accessCodeRepo.getActiveRequest() ?: return false

        // Check expiration
        if (System.currentTimeMillis() > activeRequest.expiresAt) {
            return false
        }

        // Validate code
        val isValid = userEnteredCode == activeRequest.code

        if (isValid) {
            accessCodeRepo.markRequestUsed(activeRequest.id)
        }

        return isValid
    }

    private fun generateAccessCode(): String {
        return (100000..999999).random().toString()
    }
}
```

## Data Layer

### Room Database Schema

```kotlin
@Database(
    entities = [
        BlockedDomain::class,
        TimeWindow::class,
        Violation::class,
        TempUnblock::class,
        AppUsage::class
    ],
    version = 1
)
abstract class GlockerDatabase : RoomDatabase() {
    abstract fun domainDao(): DomainDao
    abstract fun violationDao(): ViolationDao
    abstract fun unblockDao(): UnblockDao
    abstract fun appUsageDao(): AppUsageDao
}

@Entity(tableName = "blocked_domains")
data class BlockedDomain(
    @PrimaryKey val name: String,
    val alwaysBlock: Boolean,
    val absolute: Boolean,
    val enabled: Boolean = true,
    val addedAt: Long = System.currentTimeMillis()
)

@Entity(tableName = "time_windows")
data class TimeWindow(
    @PrimaryKey(autoGenerate = true) val id: Long = 0,
    val domainName: String,
    val daysOfWeek: String, // JSON array: ["Mon", "Tue"]
    val startTime: String,  // HH:mm
    val endTime: String     // HH:mm
)

@Entity(tableName = "violations")
data class Violation(
    @PrimaryKey(autoGenerate = true) val id: Long = 0,
    val type: String,        // "web_access", "app_launch", "content_keyword"
    val domain: String,
    val url: String,
    val timestamp: Long = System.currentTimeMillis()
)

@Entity(tableName = "temp_unblocks")
data class TempUnblock(
    @PrimaryKey val domain: String,
    val reason: String,
    val requestedAt: Long,
    val expiresAt: Long
)
```

### SharedPreferences Settings

```kotlin
object GlockerPreferences {
    const val PREF_NAME = "glocker_prefs"

    // Enforcement settings
    const val KEY_ENFORCEMENT_ENABLED = "enforcement_enabled"
    const val KEY_VPN_ENABLED = "vpn_enabled"
    const val KEY_DEVICE_ADMIN_ENABLED = "device_admin_enabled"

    // Accountability
    const val KEY_ACCOUNTABILITY_PARTNER_EMAIL = "partner_email"
    const val KEY_ACCOUNTABILITY_PARTNER_PHONE = "partner_phone"
    const val KEY_ACCESS_CODE_REQUIRED = "access_code_required"

    // Violation tracking
    const val KEY_VIOLATION_TRACKING_ENABLED = "violation_tracking"
    const val KEY_VIOLATION_THRESHOLD = "violation_threshold"
    const val KEY_VIOLATION_TIME_WINDOW_MINUTES = "violation_time_window"
    const val KEY_VIOLATION_ACTION_COMMAND = "violation_action"

    // Notification settings
    const val KEY_NOTIFY_ON_BLOCK = "notify_on_block"
    const val KEY_NOTIFY_PARTNER_ON_VIOLATION = "notify_partner_violation"
}
```

## Blocking Logic (Ported from Go)

### Domain Matching

```kotlin
object DomainMatcher {

    /**
     * Check if host matches blocked domain
     * Ported from internal/enforcement/domains.go
     */
    fun isBlocked(
        host: String,
        blockedDomains: List<BlockedDomain>,
        timeWindowDomains: List<DomainWithWindows>,
        currentTime: Long
    ): BlockResult {

        val normalizedHost = host.lowercase().removePrefix("www.")

        // Check always-blocked domains first
        blockedDomains.forEach { domain ->
            if (matches(normalizedHost, domain.name)) {
                return BlockResult.Blocked(
                    domain = domain.name,
                    reason = if (domain.absolute) {
                        "always blocked (absolute)"
                    } else {
                        "always blocked"
                    },
                    absolute = domain.absolute
                )
            }
        }

        // Check time-window domains
        val currentDay = getCurrentDayOfWeek()
        val currentTimeStr = getCurrentTimeString()

        timeWindowDomains.forEach { domainWithWindows ->
            if (matches(normalizedHost, domainWithWindows.domain.name)) {
                // Check if currently in blocking window
                val activeWindow = domainWithWindows.windows.firstOrNull { window ->
                    window.daysOfWeek.contains(currentDay) &&
                    isInTimeWindow(currentTimeStr, window.startTime, window.endTime)
                }

                if (activeWindow != null) {
                    return BlockResult.Blocked(
                        domain = domainWithWindows.domain.name,
                        reason = "time-based block (${activeWindow.startTime}-${activeWindow.endTime})",
                        absolute = false
                    )
                }
            }
        }

        return BlockResult.NotBlocked
    }

    private fun matches(host: String, domain: String): Boolean {
        val normalizedDomain = domain.lowercase().removePrefix("www.")

        return host == normalizedDomain ||
               host.endsWith(".$normalizedDomain")
    }

    /**
     * Time window logic - direct port from internal/utils/time.go
     */
    private fun isInTimeWindow(current: String, start: String, end: String): Boolean {
        return if (start <= end) {
            // Normal window: 09:00 to 17:00
            current >= start && current <= end
        } else {
            // Midnight-crossing: 22:00 to 05:00
            current >= start || current <= end
        }
    }
}

sealed class BlockResult {
    object NotBlocked : BlockResult()
    data class Blocked(
        val domain: String,
        val reason: String,
        val absolute: Boolean
    ) : BlockResult()
}
```

### Keyword Detection

```kotlin
object KeywordDetector {

    fun containsBlockedKeyword(
        text: String,
        keywords: List<String>,
        caseSensitive: Boolean = false
    ): String? {

        val searchText = if (caseSensitive) text else text.lowercase()

        keywords.forEach { keyword ->
            val searchKeyword = if (caseSensitive) keyword else keyword.lowercase()

            if (searchText.contains(searchKeyword)) {
                return keyword
            }
        }

        return null
    }

    fun containsBlockedPattern(
        text: String,
        patterns: List<Regex>
    ): Regex? {

        patterns.forEach { pattern ->
            if (pattern.containsMatchIn(text)) {
                return pattern
            }
        }

        return null
    }
}
```

## UI Layer (Jetpack Compose)

### Main Dashboard

```kotlin
@Composable
fun DashboardScreen(viewModel: MainViewModel) {
    val state by viewModel.state.collectAsState()

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Glocker") },
                actions = {
                    IconButton(onClick = { /* Settings */ }) {
                        Icon(Icons.Default.Settings, "Settings")
                    }
                }
            )
        }
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState())
        ) {
            // Status Card
            StatusCard(
                isEnforcementActive = state.enforcementActive,
                blockedDomainsCount = state.blockedDomainsCount,
                activeUnblocksCount = state.activeUnblocksCount
            )

            // Violation Tracking Card
            if (state.violationTrackingEnabled) {
                ViolationCard(
                    recentViolations = state.recentViolations,
                    threshold = state.violationThreshold
                )
            }

            // Quick Actions
            QuickActionsCard(
                onTempUnblock = { /* Navigate to unblock screen */ },
                onPanicMode = { viewModel.activatePanicMode() }
            )

            // Temp Unblocks List
            if (state.activeUnblocks.isNotEmpty()) {
                ActiveUnblocksCard(state.activeUnblocks)
            }
        }
    }
}
```

### Domain Management

```kotlin
@Composable
fun DomainManagementScreen(viewModel: DomainViewModel) {
    val domains by viewModel.domains.collectAsState()

    Column(modifier = Modifier.fillMaxSize()) {
        // Search bar
        OutlinedTextField(
            value = viewModel.searchQuery,
            onValueChange = { viewModel.updateSearchQuery(it) },
            modifier = Modifier
                .fillMaxWidth()
                .padding(16.dp),
            placeholder = { Text("Search domains...") },
            leadingIcon = { Icon(Icons.Default.Search, null) }
        )

        // Domains list
        LazyColumn(
            modifier = Modifier.weight(1f)
        ) {
            items(domains) { domain ->
                DomainListItem(
                    domain = domain,
                    onToggle = { viewModel.toggleDomain(domain.name) },
                    onEdit = { viewModel.editDomain(domain) },
                    onDelete = { viewModel.deleteDomain(domain.name) }
                )
            }
        }

        // FAB to add domain
        FloatingActionButton(
            onClick = { viewModel.showAddDomainDialog() },
            modifier = Modifier
                .align(Alignment.End)
                .padding(16.dp)
        ) {
            Icon(Icons.Default.Add, "Add domain")
        }
    }
}
```

## Permissions & Setup Flow

### Required Permissions

```xml
<manifest xmlns:android="http://schemas.android.com/apk/res/android">

    <!-- Core permissions -->
    <uses-permission android:name="android.permission.INTERNET" />
    <uses-permission android:name="android.permission.FOREGROUND_SERVICE" />
    <uses-permission android:name="android.permission.POST_NOTIFICATIONS" />

    <!-- Accessibility for content monitoring -->
    <uses-permission android:name="android.permission.BIND_ACCESSIBILITY_SERVICE"
        tools:ignore="ProtectedPermissions" />

    <!-- VPN (optional) -->
    <uses-permission android:name="android.permission.BIND_VPN_SERVICE"
        tools:ignore="ProtectedPermissions" />

    <!-- System overlay for blocking screen -->
    <uses-permission android:name="android.permission.SYSTEM_ALERT_WINDOW" />

    <!-- Device admin for uninstall protection -->
    <uses-permission android:name="android.permission.BIND_DEVICE_ADMIN"
        tools:ignore="ProtectedPermissions" />

    <!-- App usage stats -->
    <uses-permission android:name="android.permission.PACKAGE_USAGE_STATS"
        tools:ignore="ProtectedPermissions" />

    <!-- Boot receiver -->
    <uses-permission android:name="android.permission.RECEIVE_BOOT_COMPLETED" />

    <application>
        <!-- Services -->
        <service
            android:name=".service.GlockerAccessibilityService"
            android:permission="android.permission.BIND_ACCESSIBILITY_SERVICE"
            android:exported="true">
            <intent-filter>
                <action android:name="android.accessibilityservice.AccessibilityService" />
            </intent-filter>
            <meta-data
                android:name="android.accessibilityservice"
                android:resource="@xml/accessibility_service_config" />
        </service>

        <service
            android:name=".service.GlockerVpnService"
            android:permission="android.permission.BIND_VPN_SERVICE"
            android:exported="true">
            <intent-filter>
                <action android:name="android.net.VpnService" />
            </intent-filter>
        </service>

        <service
            android:name=".service.EnforcementForegroundService"
            android:foregroundServiceType="dataSync"
            android:exported="false" />

        <!-- Device Admin -->
        <receiver
            android:name=".admin.GlockerDeviceAdminReceiver"
            android:permission="android.permission.BIND_DEVICE_ADMIN"
            android:exported="true">
            <meta-data
                android:name="android.app.device_admin"
                android:resource="@xml/device_admin_policies" />
            <intent-filter>
                <action android:name="android.app.action.DEVICE_ADMIN_ENABLED" />
            </intent-filter>
        </receiver>

        <!-- Boot receiver -->
        <receiver
            android:name=".receiver.BootReceiver"
            android:enabled="true"
            android:exported="true">
            <intent-filter>
                <action android:name="android.intent.action.BOOT_COMPLETED" />
            </intent-filter>
        </receiver>
    </application>
</manifest>
```

### Setup Flow

```kotlin
sealed class SetupStep {
    object Welcome : SetupStep()
    object ExplainAccessibility : SetupStep()
    object RequestAccessibility : SetupStep()
    object ExplainDeviceAdmin : SetupStep()
    object RequestDeviceAdmin : SetupStep()
    object ExplainOverlay : SetupStep()
    object RequestOverlay : SetupStep()
    object ConfigureAccountability : SetupStep()
    object ImportDomains : SetupStep()
    object Complete : SetupStep()
}

@Composable
fun SetupWizard(viewModel: SetupViewModel) {
    val currentStep by viewModel.currentStep.collectAsState()

    when (currentStep) {
        is SetupStep.Welcome -> WelcomeScreen(
            onContinue = { viewModel.nextStep() }
        )

        is SetupStep.ExplainAccessibility -> ExplainPermissionScreen(
            title = "Content Monitoring",
            description = "Glocker needs Accessibility permission to monitor browser URLs and detect blocked content.",
            icon = Icons.Default.Visibility,
            onContinue = { viewModel.nextStep() }
        )

        is SetupStep.RequestAccessibility -> RequestPermissionScreen(
            permissionType = PermissionType.Accessibility,
            onGranted = { viewModel.nextStep() },
            onSkipped = { viewModel.skipOptional() }
        )

        // ... other steps
    }
}
```

## Configuration Import/Export

### YAML Config Support

```kotlin
object ConfigImporter {

    /**
     * Import from Linux Glocker config.yaml
     */
    suspend fun importFromYaml(yamlContent: String): ImportResult {
        return try {
            val config = parseYamlConfig(yamlContent)

            // Import domains
            val domains = config.domains.map { domain ->
                BlockedDomain(
                    name = domain.name,
                    alwaysBlock = domain.always_block,
                    absolute = domain.absolute
                )
            }

            domainRepository.insertAll(domains)

            // Import time windows
            val timeWindows = config.domains.flatMap { domain ->
                domain.time_windows.map { window ->
                    TimeWindow(
                        domainName = domain.name,
                        daysOfWeek = Json.encodeToString(window.days),
                        startTime = window.start,
                        endTime = window.end
                    )
                }
            }

            timeWindowRepository.insertAll(timeWindows)

            // Import settings
            importSettings(config)

            ImportResult.Success(
                domainsImported = domains.size,
                timeWindowsImported = timeWindows.size
            )

        } catch (e: Exception) {
            ImportResult.Error(e.message ?: "Unknown error")
        }
    }
}
```

## Testing Strategy

### Unit Tests

```kotlin
class DomainMatcherTest {

    @Test
    fun `test direct domain match`() {
        val result = DomainMatcher.isBlocked(
            host = "example.com",
            blockedDomains = listOf(
                BlockedDomain(name = "example.com", alwaysBlock = true, absolute = false)
            ),
            timeWindowDomains = emptyList(),
            currentTime = System.currentTimeMillis()
        )

        assertTrue(result is BlockResult.Blocked)
        assertEquals("example.com", (result as BlockResult.Blocked).domain)
    }

    @Test
    fun `test subdomain match`() {
        val result = DomainMatcher.isBlocked(
            host = "api.example.com",
            blockedDomains = listOf(
                BlockedDomain(name = "example.com", alwaysBlock = true, absolute = false)
            ),
            timeWindowDomains = emptyList(),
            currentTime = System.currentTimeMillis()
        )

        assertTrue(result is BlockResult.Blocked)
    }

    @Test
    fun `test time window blocking`() {
        val currentTime = LocalDateTime.of(2024, 1, 15, 14, 30) // Monday 14:30

        val result = DomainMatcher.isBlocked(
            host = "twitter.com",
            blockedDomains = emptyList(),
            timeWindowDomains = listOf(
                DomainWithWindows(
                    domain = BlockedDomain(name = "twitter.com", alwaysBlock = false, absolute = false),
                    windows = listOf(
                        TimeWindow(
                            domainName = "twitter.com",
                            daysOfWeek = """["Mon","Tue","Wed","Thu","Fri"]""",
                            startTime = "09:00",
                            endTime = "17:00"
                        )
                    )
                )
            ),
            currentTime = currentTime.toMillis()
        )

        assertTrue(result is BlockResult.Blocked)
    }
}
```

### Integration Tests

```kotlin
@RunWith(AndroidJUnit4::class)
class AccessibilityServiceTest {

    @Test
    fun testUrlExtraction() {
        // Launch Chrome
        val intent = Intent(Intent.ACTION_VIEW, Uri.parse("https://example.com"))
        intent.setPackage("com.android.chrome")
        context.startActivity(intent)

        // Wait for accessibility event
        Thread.sleep(2000)

        // Verify URL was detected
        val detectedUrl = accessibilityService.lastDetectedUrl
        assertEquals("https://example.com", detectedUrl)
    }
}
```

## Performance Considerations

### Battery Optimization

1. **AccessibilityService**
   - Only process `TYPE_WINDOW_CONTENT_CHANGED` when window is active
   - Debounce events (100ms) to avoid duplicate processing
   - Cache browser package name to avoid repeated lookups

2. **VPN Service**
   - Use efficient packet parsing (ByteBuffer, not String operations)
   - Maintain small in-memory cache of DNS resolutions
   - Batch write operations to TUN interface

3. **Foreground Service**
   - Use WorkManager for scheduling (more battery-efficient)
   - Only wake up when necessary (time window boundaries)
   - Batch database operations

### Memory Optimization

1. **Domain Storage**
   - Store 800K+ domains in SQLite with FTS (Full-Text Search)
   - Keep only time-window domains in memory
   - Use paging for UI lists (Paging 3)

2. **Node Recycling**
   - Always call `AccessibilityNodeInfo.recycle()` after use
   - Avoid keeping node references

3. **Bitmap Handling**
   - If doing OCR, use `BitmapFactory.Options.inSampleSize`
   - Recycle bitmaps immediately after use

## Deployment

### Build Variants

```groovy
android {
    buildTypes {
        debug {
            applicationIdSuffix ".debug"
            debuggable true
        }

        release {
            minifyEnabled true
            shrinkResources true
            proguardFiles getDefaultProguardFile('proguard-android-optimize.txt'),
                         'proguard-rules.pro'
            signingConfig signingConfigs.release
        }
    }

    flavorDimensions "version"
    productFlavors {
        free {
            dimension "version"
            applicationIdSuffix ".free"

            buildConfigField "int", "MAX_DOMAINS", "100"
            buildConfigField "boolean", "VPN_ENABLED", "false"
        }

        premium {
            dimension "version"

            buildConfigField "int", "MAX_DOMAINS", "999999"
            buildConfigField "boolean", "VPN_ENABLED", "true"
        }
    }
}
```

### Distribution Channels

1. **Google Play Store**
   - Primary distribution
   - Requires privacy policy
   - Accessibility service requires justification
   - Review time: 3-7 days

2. **F-Droid**
   - Open-source builds
   - No tracking/analytics
   - Community-trusted

3. **Direct APK**
   - GitHub Releases
   - Self-hosting option
   - Requires "Unknown sources" enabled

## Migration Path from Linux

For existing Glocker users:

1. **Export Linux config**
   ```bash
   cp /etc/glocker/config.yaml ~/glocker-backup.yaml
   ```

2. **Transfer to Android**
   - Email to self
   - Cloud storage (Dropbox, Google Drive)
   - USB transfer

3. **Import in Android app**
   - App reads YAML directly
   - Converts to SQLite format
   - Preserves all time windows, domains, settings

## Future Enhancements

### Phase 2 Features
- [ ] DNS-over-HTTPS blocking
- [ ] Custom blocking screens with motivational messages
- [ ] Screen time limits per app
- [ ] Website whitelist mode
- [ ] Focus mode with Pomodoro timer

### Phase 3 Features
- [ ] Family plan (parent controls child's device)
- [ ] Sync config across devices (cloud backup)
- [ ] Website categories (social media, news, shopping)
- [ ] Screen time analytics dashboard
- [ ] Integration with external accountability services

## References

- [Android AccessibilityService Guide](https://developer.android.com/guide/topics/ui/accessibility/service)
- [Android VpnService Documentation](https://developer.android.com/reference/android/net/VpnService)
- [Device Administration](https://developer.android.com/guide/topics/admin/device-admin)
- BlockerX Android app analysis
- Glocker Linux codebase (reference implementation)

---

**Document Version:** 1.0
**Last Updated:** 2026-01-15
**Status:** Design Document (Not Implemented)
