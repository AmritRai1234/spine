plugins {
    kotlin("jvm") version "1.9.22"
}

group = "dev.spine"
version = "2.4.0"

repositories {
    mavenCentral()
}

dependencies {
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-core:1.8.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.0")
    implementation("org.json:json:20231013")
    testImplementation(kotlin("test"))
}
