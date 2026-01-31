func Reader() {
    for {
        m3.P()
            reader.P()
                m1.P()
                    nr++
                    if nr == 1 {
                        writer.P()
                    }
                m1.V()
            reader.V()
        m3.V()
        // Access the data base
        m1.P()
            nr--
            if nr == 0 {
                writer.V()
            }
        m1.V()
    }
} 

func Writer() {
    for {
        m2.P()
            nw++
            if nw == 1 {
                reader.P()
            }
        m2.V()
        writer.P()
        // Read and write the data base
        writer.V()
        m2.P()
            nw--
            if nw == 0 {
                reader.V()
            }
        m2.V()
    }
}